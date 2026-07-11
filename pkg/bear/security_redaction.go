package bear

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

const (
	redactedObservableValue  = "[REDACTED]"
	truncatedObservableValue = "[TRUNCATED]"
	cyclicObservableValue    = "[CYCLE]"
	maxObservableDepth       = 6
	maxObservableCollection  = 16
	maxObservableStringBytes = 4096
	maxObservableErrorDepth  = 32
	maxObservableErrorNodes  = 64
)

var (
	observableURLPattern  = regexp.MustCompile(`(?i)\b(?:https?|postgres(?:ql)?|mysql)://[^\s,;]+`)
	mysqlDSNPattern       = regexp.MustCompile(`(?i)\b([^:\s]+):([^@\s]+)@(tcp|unix)\(([^)]*)\)/([^\s?]*)(?:\?[^\s,;]*)?`)
	authorizationPattern  = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;]+`)
	sensitiveValuePattern = regexp.MustCompile(
		`(?i)\b(password|passwd|pwd|token|access_token|refresh_token|secret|client_secret|api_key|apikey|cookie)\s*([=:])\s*(?:'[^']*'|"[^"]*"|[^\s,;]+)`,
	)
	jwtValuePattern = regexp.MustCompile(`\b[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
)

// SanitizeForObservability converts arbitrary boundary values to a bounded
// category suitable for panic and health diagnostics. Structured log values
// use the recursive sanitizer below so safe context can be retained.
func SanitizeForObservability(value any) string {
	if value == nil {
		return "none"
	}
	if err, ok := value.(error); ok {
		category, _ := observableErrorMetadata(err, 0)
		return category
	}
	return redactedObservableValue
}

func observableErrorMetadata(err error, fallbackStatus int) (string, int) {
	code := fallbackStatus
	if err == nil {
		return "none", code
	}

	var (
		bearError        *BearError
		contextCanceled  bool
		deadlineExceeded bool
		tokenExpired     bool
		tokenMissing     bool
		tokenInvalid     bool
	)
	walkObservableErrors(err, func(current error) bool {
		if typed, ok := current.(*BearError); ok && typed != nil {
			bearError = typed
			return true
		}
		switch {
		case observableErrorsEqual(current, context.Canceled):
			contextCanceled = true
		case observableErrorsEqual(current, context.DeadlineExceeded):
			deadlineExceeded = true
		case observableErrorsEqual(current, jwt.ErrTokenExpired):
			tokenExpired = true
		case observableErrorsEqual(current, jwt.ErrTokenRequiredClaimMissing):
			tokenMissing = true
		case observableErrorsEqual(current, jwt.ErrTokenMalformed),
			observableErrorsEqual(current, jwt.ErrTokenSignatureInvalid),
			observableErrorsEqual(current, jwt.ErrTokenInvalidClaims):
			tokenInvalid = true
		}
		return false
	})

	if bearError != nil {
		if bearError.Code != 0 {
			code = bearError.Code
		} else if fallbackStatus == 0 && bearError.Status != 0 {
			code = bearError.Status
		}
		return "bear_error", code
	}

	switch {
	case contextCanceled:
		return "context_canceled", code
	case deadlineExceeded:
		return "deadline_exceeded", code
	case tokenExpired:
		return "token_expired", code
	case tokenMissing:
		return "token_missing_claim", code
	case tokenInvalid:
		return "token_invalid", code
	case fallbackStatus >= http.StatusBadRequest && fallbackStatus < http.StatusInternalServerError:
		return "request_error", code
	default:
		return "internal_error", code
	}
}

type observableErrorNode struct {
	err   error
	depth int
}

type observableErrorVisit struct {
	typ reflect.Type
	ptr uintptr
}

// walkObservableErrors only follows standard unwrap interfaces under explicit
// bounds. It never invokes Error, String, Is, or As on an untrusted error.
func walkObservableErrors(root error, visit func(error) bool) {
	queue := []observableErrorNode{{err: root}}
	seen := make(map[observableErrorVisit]struct{})
	visited := 0
	for len(queue) > 0 && visited < maxObservableErrorNodes {
		node := queue[0]
		queue = queue[1:]
		if node.err == nil {
			continue
		}
		if identity, ok := observableErrorIdentity(node.err); ok {
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
		}
		visited++
		if visit(node.err) || node.depth >= maxObservableErrorDepth {
			continue
		}
		for _, child := range observableErrorChildren(node.err) {
			if child != nil && len(queue) < maxObservableErrorNodes {
				queue = append(queue, observableErrorNode{err: child, depth: node.depth + 1})
			}
		}
	}
}

func observableErrorIdentity(err error) (observableErrorVisit, bool) {
	value := reflect.ValueOf(err)
	if !value.IsValid() {
		return observableErrorVisit{}, false
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		if value.IsNil() {
			return observableErrorVisit{}, false
		}
		if ptr := value.Pointer(); ptr != 0 {
			return observableErrorVisit{typ: value.Type(), ptr: ptr}, true
		}
	}
	return observableErrorVisit{}, false
}

func observableErrorChildren(err error) (children []error) {
	defer func() {
		if recover() != nil {
			children = nil
		}
	}()
	if multiple, ok := err.(interface{ Unwrap() []error }); ok {
		children = multiple.Unwrap()
		if len(children) > maxObservableCollection {
			children = children[:maxObservableCollection]
		}
		return children
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		if child := single.Unwrap(); child != nil {
			return []error{child}
		}
	}
	return nil
}

func observableErrorsEqual(first, second error) bool {
	if first == nil || second == nil {
		return false
	}
	typ := reflect.TypeOf(first)
	if typ != reflect.TypeOf(second) || !typ.Comparable() {
		return false
	}
	return first == second
}

func sanitizeObservableString(value string) string {
	value = truncateObservableString(value)
	value = observableURLPattern.ReplaceAllStringFunc(value, sanitizeURLText)
	value = mysqlDSNPattern.ReplaceAllString(value, `${1}:`+redactedObservableValue+`@${3}(${4})/${5}`)
	value = authorizationPattern.ReplaceAllString(value, `${1}`+redactedObservableValue)
	value = sensitiveValuePattern.ReplaceAllString(value, `${1}${2}`+redactedObservableValue)
	return jwtValuePattern.ReplaceAllString(value, redactedObservableValue)
}

func sanitizeURLText(raw string) string {
	core, suffix := trimURLSuffix(raw)
	parsed, err := url.Parse(core)
	if err != nil || parsed.Scheme == "" {
		return redactedObservableValue + suffix
	}
	return safeURLString(*parsed) + suffix
}

func trimURLSuffix(raw string) (string, string) {
	trimmed := strings.TrimRight(raw, `"')].`)
	return trimmed, raw[len(trimmed):]
}

func safeURLString(value url.URL) string {
	safe := url.URL{
		Scheme:  value.Scheme,
		Host:    value.Host,
		Path:    value.Path,
		RawPath: value.RawPath,
	}
	return safe.String()
}

func truncateObservableString(value string) string {
	if len(value) <= maxObservableStringBytes {
		return value
	}
	end := maxObservableStringBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + truncatedObservableValue
}

type observableVisit struct {
	typ reflect.Type
	ptr uintptr
}

type observableRedactionState struct {
	seen map[observableVisit]struct{}
}

func newObservableRedactionState() *observableRedactionState {
	return &observableRedactionState{seen: make(map[observableVisit]struct{})}
}

func sanitizeLogRecord(record slog.Record) slog.Record {
	sanitized := slog.NewRecord(record.Time, record.Level, sanitizeObservableString(record.Message), record.PC)
	state := newObservableRedactionState()
	record.Attrs(func(attr slog.Attr) bool {
		sanitized.AddAttrs(sanitizeLogAttrAt(attr, state, 0))
		return true
	})
	return sanitized
}

func sanitizeLogAttrs(attrs []slog.Attr) []slog.Attr {
	return sanitizeLogAttrsAt(attrs, newObservableRedactionState(), 0)
}

func sanitizeLogAttrsAt(attrs []slog.Attr, state *observableRedactionState, depth int) []slog.Attr {
	limit := min(len(attrs), maxObservableCollection)
	sanitized := make([]slog.Attr, 0, limit+1)
	for _, attr := range attrs[:limit] {
		sanitized = append(sanitized, sanitizeLogAttrAt(attr, state, depth))
	}
	if len(attrs) > limit {
		sanitized = append(sanitized, slog.String("_truncated", truncatedObservableValue))
	}
	return sanitized
}

func sanitizeLogAttr(attr slog.Attr) slog.Attr {
	return sanitizeLogAttrAt(attr, newObservableRedactionState(), 0)
}

func sanitizeLogAttrAt(attr slog.Attr, state *observableRedactionState, depth int) slog.Attr {
	if isSensitiveObservableKey(attr.Key) {
		attr.Value = slog.StringValue(redactedObservableValue)
		return attr
	}
	if depth > maxObservableDepth {
		attr.Value = slog.StringValue(truncatedObservableValue)
		return attr
	}

	attr.Value = attr.Value.Resolve()
	if isErrorObservableKey(attr.Key) {
		attr.Value = slog.StringValue(SanitizeForObservability(attr.Value.Any()))
		return attr
	}

	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(sanitizeObservableString(attr.Value.String()))
	case slog.KindGroup:
		attr.Value = slog.GroupValue(sanitizeLogAttrsAt(attr.Value.Group(), state, depth+1)...)
	case slog.KindAny:
		attr.Value = slog.AnyValue(sanitizeStructuredValue(attr.Key, attr.Value.Any(), state, depth+1))
	}
	return attr
}

func sanitizeStructuredValue(key string, value any, state *observableRedactionState, depth int) any {
	if isSensitiveObservableKey(key) {
		return redactedObservableValue
	}
	if depth > maxObservableDepth {
		return truncatedObservableValue
	}
	if value == nil {
		return nil
	}

	switch typed := value.(type) {
	case error:
		return SanitizeForObservability(typed)
	case slog.Value:
		return sanitizeNestedSlogValue(typed, state, depth)
	case url.URL:
		return safeURLString(typed)
	case *url.URL:
		if typed == nil {
			return nil
		}
		return safeURLString(*typed)
	case http.Header:
		return sanitizeHTTPHeader(typed, state, depth)
	case *http.Header:
		if typed == nil {
			return nil
		}
		return sanitizeHTTPHeader(*typed, state, depth)
	case string:
		return sanitizeObservableString(typed)
	case []byte:
		return sanitizeObservableString(string(typed))
	case time.Time, time.Duration:
		return typed
	}

	reflected := reflect.ValueOf(value)
	return sanitizeReflectedValue(key, reflected, state, depth)
}

func sanitizeNestedSlogValue(value slog.Value, state *observableRedactionState, depth int) any {
	value = value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return sanitizeObservableString(value.String())
	case slog.KindGroup:
		result := make(map[string]any)
		for _, attr := range sanitizeLogAttrsAt(value.Group(), state, depth+1) {
			result[attr.Key] = sanitizedSlogValueAny(attr.Value, state, depth+1)
		}
		return result
	case slog.KindAny:
		return sanitizeStructuredValue("", value.Any(), state, depth+1)
	default:
		return value.Any()
	}
}

func sanitizedSlogValueAny(value slog.Value, state *observableRedactionState, depth int) any {
	value = value.Resolve()
	if value.Kind() != slog.KindGroup {
		return value.Any()
	}
	result := make(map[string]any)
	for _, attr := range sanitizeLogAttrsAt(value.Group(), state, depth+1) {
		result[attr.Key] = sanitizedSlogValueAny(attr.Value, state, depth+1)
	}
	return result
}

func sanitizeHTTPHeader(header http.Header, state *observableRedactionState, depth int) map[string]any {
	keys := make([]string, 0, min(len(header), maxObservableCollection+1))
	for key := range header {
		keys = append(keys, key)
		if len(keys) > maxObservableCollection {
			break
		}
	}
	sort.Strings(keys)
	limit := min(len(keys), maxObservableCollection)
	result := make(map[string]any, limit+1)
	for _, key := range keys[:limit] {
		result[key] = sanitizeStructuredValue(key, header[key], state, depth+1)
	}
	if len(header) > maxObservableCollection {
		result["_truncated"] = truncatedObservableValue
	}
	return result
}

func sanitizeReflectedValue(key string, value reflect.Value, state *observableRedactionState, depth int) any {
	if !value.IsValid() {
		return nil
	}
	if depth > maxObservableDepth {
		return truncatedObservableValue
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return sanitizeReflectedValue(key, value.Elem(), state, depth)
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		if !state.enter(value) {
			return cyclicObservableValue
		}
		defer state.leave(value)
		return sanitizeReflectedValue(key, value.Elem(), state, depth+1)
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if !state.enter(value) {
			return cyclicObservableValue
		}
		defer state.leave(value)
		return sanitizeReflectedMap(value, state, depth)
	case reflect.Struct:
		return sanitizeReflectedStruct(value, state, depth)
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		if !state.enter(value) {
			return cyclicObservableValue
		}
		defer state.leave(value)
		return sanitizeReflectedCollection(value, state, depth)
	case reflect.Array:
		return sanitizeReflectedCollection(value, state, depth)
	case reflect.String:
		return sanitizeObservableString(value.String())
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	default:
		return "[UNSUPPORTED]"
	}
}

func sanitizeReflectedMap(value reflect.Value, state *observableRedactionState, depth int) map[string]any {
	type mapEntry struct {
		name      string
		value     any
		sensitive bool
	}
	entries := make([]mapEntry, 0, min(value.Len(), maxObservableCollection+1))
	iterator := value.MapRange()
	for len(entries) <= maxObservableCollection && iterator.Next() {
		name, sensitive := sanitizeObservableMapKey(iterator.Key(), len(entries))
		mapValue := iterator.Value()
		if mapValue.IsValid() && mapValue.CanInterface() {
			entries = append(entries, mapEntry{name: name, value: mapValue.Interface(), sensitive: sensitive})
		} else {
			entries = append(entries, mapEntry{name: name, value: "[UNSUPPORTED]", sensitive: sensitive})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	limit := min(len(entries), maxObservableCollection)
	result := make(map[string]any, limit+1)
	for _, entry := range entries[:limit] {
		if entry.sensitive {
			result[entry.name] = redactedObservableValue
		} else {
			result[entry.name] = sanitizeStructuredValue(entry.name, entry.value, state, depth+1)
		}
	}
	if value.Len() > maxObservableCollection {
		result["_truncated"] = truncatedObservableValue
	}
	return result
}

func sanitizeObservableMapKey(value reflect.Value, index int) (string, bool) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return observableMapKeyPlaceholder(value, index), false
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return "[KEY invalid #" + strconv.Itoa(index) + "]", false
	}

	var name string
	switch value.Kind() {
	case reflect.String:
		name = value.String()
	case reflect.Bool:
		name = strconv.FormatBool(value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		name = strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		name = strconv.FormatUint(value.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		name = strconv.FormatFloat(value.Float(), 'g', -1, value.Type().Bits())
	default:
		name = observableMapKeyPlaceholder(value, index)
	}
	name = truncateObservableString(name)
	if isSensitiveObservableMapKey(name) {
		return redactedObservableValue, true
	}
	return sanitizeObservableString(name), false
}

func observableMapKeyPlaceholder(value reflect.Value, index int) string {
	typeName := "invalid"
	if value.IsValid() {
		typeName = value.Type().String()
	}
	return "[KEY " + typeName + " #" + strconv.Itoa(index) + "]"
}

func isSensitiveObservableMapKey(key string) bool {
	if isSensitiveObservableKey(key) {
		return true
	}
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(key))
	for _, marker := range []string{"token", "password", "passwd", "secret", "authorization", "cookie", "credential", "apikey", "accesskey", "dsn", "query", "rawquery", "userinfo", "connectionstring", "stacktrace"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func sanitizeReflectedStruct(value reflect.Value, state *observableRedactionState, depth int) map[string]any {
	result := make(map[string]any)
	typ := value.Type()
	count := 0
	for i := 0; i < value.NumField(); i++ {
		fieldType := typ.Field(i)
		if fieldType.PkgPath != "" {
			continue
		}
		name, include := observableFieldName(fieldType)
		if !include {
			continue
		}
		if count >= maxObservableCollection {
			result["_truncated"] = truncatedObservableValue
			break
		}
		field := value.Field(i)
		if field.CanInterface() {
			result[name] = sanitizeStructuredValue(name, field.Interface(), state, depth+1)
		} else {
			result[name] = "[UNSUPPORTED]"
		}
		count++
	}
	return result
}

func observableFieldName(field reflect.StructField) (string, bool) {
	for _, tagName := range []string{"slog", "json"} {
		tag := strings.Split(field.Tag.Get(tagName), ",")[0]
		if tag == "-" {
			return "", false
		}
		if tag != "" {
			return tag, true
		}
	}
	return field.Name, true
}

func sanitizeReflectedCollection(value reflect.Value, state *observableRedactionState, depth int) []any {
	limit := min(value.Len(), maxObservableCollection)
	result := make([]any, 0, limit+1)
	for i := 0; i < limit; i++ {
		item := value.Index(i)
		if item.CanInterface() {
			result = append(result, sanitizeStructuredValue("", item.Interface(), state, depth+1))
		} else {
			result = append(result, "[UNSUPPORTED]")
		}
	}
	if value.Len() > limit {
		result = append(result, truncatedObservableValue)
	}
	return result
}

func (state *observableRedactionState) enter(value reflect.Value) bool {
	ptr := value.Pointer()
	if ptr == 0 {
		return true
	}
	visit := observableVisit{typ: value.Type(), ptr: ptr}
	if _, exists := state.seen[visit]; exists {
		return false
	}
	state.seen[visit] = struct{}{}
	return true
}

func (state *observableRedactionState) leave(value reflect.Value) {
	ptr := value.Pointer()
	if ptr != 0 {
		delete(state.seen, observableVisit{typ: value.Type(), ptr: ptr})
	}
}

func isSensitiveObservableKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, marker := range []string{
		"token", "password", "passwd", "secret", "authorization", "cookie",
		"credential", "apikey", "accesskey", "dsn", "query", "rawquery",
		"userinfo", "connectionstring", "stacktrace",
	} {
		if normalized == marker || strings.HasPrefix(normalized, marker) || strings.HasSuffix(normalized, marker) {
			return true
		}
	}
	return false
}

func isErrorObservableKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "error", "err", "cause", "panic", "recovered":
		return true
	default:
		return false
	}
}
