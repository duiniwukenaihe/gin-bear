package bear

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

var ginContextType = reflect.TypeOf((*gin.Context)(nil))

type argumentBinder func(*gin.Context) (reflect.Value, error)

type argumentPlan struct {
	binders []argumentBinder
}

type pathBindingError struct {
	cause error
}

func (e *pathBindingError) Error() string {
	return e.cause.Error()
}

func (e *pathBindingError) Unwrap() error {
	return e.cause
}

func compileArguments(handlerType reflect.Type) (argumentPlan, error) {
	plan := argumentPlan{binders: make([]argumentBinder, 0, handlerType.NumIn())}
	contextCount := 0
	requestCount := 0
	pathIndex := 0

	for i := 0; i < handlerType.NumIn(); i++ {
		argumentType := handlerType.In(i)
		switch {
		case argumentType == ginContextType:
			contextCount++
			if contextCount > 1 {
				return argumentPlan{}, fmt.Errorf("handler %s accepts more than one *gin.Context", handlerType)
			}
			plan.binders = append(plan.binders, func(ctx *gin.Context) (reflect.Value, error) {
				return reflect.ValueOf(ctx), nil
			})
		case argumentType.Kind() == reflect.Pointer && argumentType.Elem().Kind() == reflect.Struct:
			requestCount++
			if requestCount > 1 {
				return argumentPlan{}, fmt.Errorf("handler %s accepts more than one request struct", handlerType)
			}
			capturedType := argumentType
			plan.binders = append(plan.binders, func(ctx *gin.Context) (reflect.Value, error) {
				request := reflect.New(capturedType.Elem())
				if err := bindRequest(ctx, request.Interface()); err != nil {
					return reflect.Value{}, err
				}
				return request, nil
			})
		case argumentType.Kind() == reflect.Struct:
			requestCount++
			if requestCount > 1 {
				return argumentPlan{}, fmt.Errorf("handler %s accepts more than one request struct", handlerType)
			}
			capturedType := argumentType
			plan.binders = append(plan.binders, func(ctx *gin.Context) (reflect.Value, error) {
				request := reflect.New(capturedType)
				if err := bindRequest(ctx, request.Interface()); err != nil {
					return reflect.Value{}, err
				}
				return request.Elem(), nil
			})
		case argumentType.Kind() == reflect.String:
			capturedIndex := pathIndex
			pathIndex++
			plan.binders = append(plan.binders, pathStringBinder(argumentType, capturedIndex))
		case isSignedInteger(argumentType.Kind()):
			capturedIndex := pathIndex
			pathIndex++
			plan.binders = append(plan.binders, pathIntegerBinder(argumentType, capturedIndex))
		default:
			return argumentPlan{}, fmt.Errorf("handler %s has unsupported argument %d of type %s", handlerType, i, argumentType)
		}
	}
	return plan, nil
}

func (plan argumentPlan) Bind(ctx *gin.Context) ([]reflect.Value, error) {
	args := make([]reflect.Value, len(plan.binders))
	for i, bind := range plan.binders {
		value, err := bind(ctx)
		if err != nil {
			return nil, fmt.Errorf("bind handler argument %d: %w", i, err)
		}
		args[i] = value
	}
	return args, nil
}

func pathStringBinder(argumentType reflect.Type, index int) argumentBinder {
	return func(ctx *gin.Context) (reflect.Value, error) {
		raw, err := pathParameter(ctx, index)
		if err != nil {
			return reflect.Value{}, &pathBindingError{cause: err}
		}
		value := reflect.New(argumentType).Elem()
		value.SetString(raw)
		return value, nil
	}
}

func pathIntegerBinder(argumentType reflect.Type, index int) argumentBinder {
	return func(ctx *gin.Context) (reflect.Value, error) {
		raw, err := pathParameter(ctx, index)
		if err != nil {
			return reflect.Value{}, &pathBindingError{cause: err}
		}
		parsed, err := strconv.ParseInt(raw, 10, argumentType.Bits())
		if err != nil {
			return reflect.Value{}, &pathBindingError{cause: fmt.Errorf("invalid path parameter %q for %s: %w", raw, argumentType, err)}
		}
		value := reflect.New(argumentType).Elem()
		value.SetInt(parsed)
		return value, nil
	}
}

func pathParameter(ctx *gin.Context, index int) (string, error) {
	if index >= len(ctx.Params) || ctx.Params[index].Value == "" {
		return "", fmt.Errorf("missing path parameter at position %d", index)
	}
	return ctx.Params[index].Value, nil
}

func isSignedInteger(kind reflect.Kind) bool {
	return kind >= reflect.Int && kind <= reflect.Int64
}

func bindRequest(ctx *gin.Context, request interface{}) error {
	if err := bindQueryFields(ctx, request); err != nil {
		return err
	}
	if isFormRequest(ctx.Request) {
		if err := ctx.Request.ParseForm(); err != nil {
			return err
		}
		if err := bindFormFields(ctx, request); err != nil {
			return err
		}
	}
	if isJSONRequest(ctx.Request) && hasRequestBody(ctx.Request) {
		decoder := json.NewDecoder(ctx.Request.Body)
		if err := decoder.Decode(request); err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		var trailing interface{}
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return errors.New("request body must contain exactly one JSON value")
			}
			return fmt.Errorf("invalid trailing JSON content: %w", err)
		}
	}
	if err := bindURIFields(ctx, request); err != nil {
		return err
	}
	if binding.Validator != nil {
		return binding.Validator.ValidateStruct(request)
	}
	return nil
}

func isJSONRequest(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func isFormRequest(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "application/x-www-form-urlencoded" || mediaType == "multipart/form-data"
}

func hasRequestBody(request *http.Request) bool {
	return request.Body != nil && request.Body != http.NoBody && request.ContentLength != 0
}

func bindURIFields(ctx *gin.Context, request interface{}) error {
	values := make(map[string][]string, len(ctx.Params))
	for _, parameter := range ctx.Params {
		values[parameter.Key] = []string{parameter.Value}
	}
	return bindTaggedFields(reflect.ValueOf(request), values, "uri")
}

func bindQueryFields(ctx *gin.Context, request interface{}) error {
	if err := bindTaggedFields(reflect.ValueOf(request), ctx.Request.URL.Query(), "query"); err != nil {
		return err
	}
	return bindTaggedFields(reflect.ValueOf(request), ctx.Request.URL.Query(), "form")
}

func bindFormFields(ctx *gin.Context, request interface{}) error {
	if err := bindTaggedFields(reflect.ValueOf(request), ctx.Request.PostForm, "query"); err != nil {
		return err
	}
	return bindTaggedFields(reflect.ValueOf(request), ctx.Request.PostForm, "form")
}

func bindTaggedFields(value reflect.Value, values map[string][]string, tagName string) error {
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	value = value.Elem()
	if value.Kind() != reflect.Struct {
		return nil
	}
	valueType := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		structField := valueType.Field(i)
		if structField.PkgPath != "" {
			continue
		}
		if structField.Anonymous && field.Kind() == reflect.Struct {
			if err := bindTaggedFields(field.Addr(), values, tagName); err != nil {
				return err
			}
			continue
		}
		name := tagFieldName(structField.Tag.Get(tagName))
		if name == "" {
			continue
		}
		rawValues, ok := values[name]
		if !ok || len(rawValues) == 0 {
			continue
		}
		if err := setFieldValue(field, rawValues[0]); err != nil {
			return fmt.Errorf("bind %s field %s: %w", tagName, structField.Name, err)
		}
	}
	return nil
}

func tagFieldName(tag string) string {
	if tag == "" || tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}

func setFieldValue(field reflect.Value, raw string) error {
	if !field.CanSet() || raw == "" {
		return nil
	}
	if field.Kind() == reflect.Pointer {
		element := reflect.New(field.Type().Elem())
		if err := setFieldValue(element.Elem(), raw); err != nil {
			return err
		}
		field.Set(element)
		return nil
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		field.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(raw, 10, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(raw, 10, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(raw, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetFloat(value)
	}
	return nil
}
