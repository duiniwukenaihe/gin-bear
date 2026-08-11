package bear

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthorizationRequest describes one resource-level authorization decision.
type AuthorizationRequest struct {
	Subject  string
	Resource string
	Action   string
	Scope    map[string]string
}

// Authorizer decides whether a subject may perform an action on a resource.
type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) (bool, error)
}

// ScopeResolver derives resource scope from an authenticated request.
type ScopeResolver func(*gin.Context) (map[string]string, error)

// SubjectResolver derives the authorization subject from a request.
type SubjectResolver func(*gin.Context) (string, error)

// PermissionFairing performs storage-independent resource authorization.
type PermissionFairing struct {
	BaseFairing
	Authorizer Authorizer `inject:"-"`

	resource        string
	action          string
	scopeResolver   ScopeResolver
	subjectResolver SubjectResolver
	defaultSubject  bool
}

// NewPermissionFairing creates a resource-level authorization Fairing. The
// default authenticated-subject resolver requires strict Fairing ordering.
func NewPermissionFairing(resource, action string, scope ScopeResolver) *PermissionFairing {
	return &PermissionFairing{
		resource:        strings.TrimSpace(resource),
		action:          strings.TrimSpace(action),
		scopeResolver:   scope,
		subjectResolver: defaultSubjectResolver,
		defaultSubject:  true,
	}
}

// SetSubjectResolver replaces the default current_user_id resolver.
func (p *PermissionFairing) SetSubjectResolver(resolver SubjectResolver) *PermissionFairing {
	if resolver == nil {
		p.subjectResolver = defaultSubjectResolver
		p.defaultSubject = true
	} else {
		p.subjectResolver = resolver
		p.defaultSubject = false
	}
	return p
}

func (p *PermissionFairing) OnRequest(ctx *gin.Context) error {
	if p == nil || ctx == nil {
		return ErrInternalServer
	}
	if p.resource == "" || p.action == "" || p.Authorizer == nil {
		permissionLogger(ctx).ErrorContext(requestContext(ctx), "Permission Fairing is not configured",
			"resource", p.resource,
			"action", p.action,
			"authorizer_configured", p.Authorizer != nil,
		)
		return ErrInternalServer
	}
	if p.defaultSubject && !permissionStrictRuntime(ctx) {
		permissionLogger(ctx).ErrorContext(requestContext(ctx), "Default authorization subject requires strict Fairing order",
			"resource", p.resource,
			"action", p.action,
		)
		return ErrInternalServer
	}

	resolver := p.subjectResolver
	if resolver == nil {
		resolver = defaultSubjectResolver
	}
	subject, err := resolver(ctx)
	if err != nil {
		permissionLogger(ctx).ErrorContext(requestContext(ctx), "Authorization subject resolution failed",
			"error", err,
			"resource", p.resource,
			"action", p.action,
		)
		return ErrInternalServer
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ErrUnauthorized
	}

	scope := map[string]string(nil)
	if p.scopeResolver != nil {
		resolved, resolveErr := p.scopeResolver(ctx)
		if resolveErr != nil {
			permissionLogger(ctx).ErrorContext(requestContext(ctx), "Authorization scope resolution failed",
				"error", resolveErr,
				"resource", p.resource,
				"action", p.action,
			)
			return ErrInternalServer
		}
		scope = cloneAuthorizationScope(resolved)
	}

	allowed, err := p.Authorizer.Authorize(requestContext(ctx), AuthorizationRequest{
		Subject:  subject,
		Resource: p.resource,
		Action:   p.action,
		Scope:    scope,
	})
	if err != nil {
		permissionLogger(ctx).ErrorContext(requestContext(ctx), "Authorization decision failed",
			"error", err,
			"resource", p.resource,
			"action", p.action,
		)
		return ErrInternalServer
	}
	if !allowed {
		permissionLogger(ctx).WarnContext(requestContext(ctx), "Authorization denied",
			"resource", p.resource,
			"action", p.action,
		)
		return ErrForbidden
	}
	return nil
}

func (p *PermissionFairing) Name() string {
	if p == nil {
		return "PermissionFairing"
	}
	return fmt.Sprintf("PermissionFairing[%s:%s]", p.resource, p.action)
}

func defaultSubjectResolver(ctx *gin.Context) (string, error) {
	if ctx == nil {
		return "", nil
	}
	value, exists := ctx.Get("current_user_id")
	if !exists || value == nil {
		return "", nil
	}
	switch subject := value.(type) {
	case string:
		return subject, nil
	case []byte:
		return string(subject), nil
	case int:
		return strconv.Itoa(subject), nil
	case int8:
		return strconv.FormatInt(int64(subject), 10), nil
	case int16:
		return strconv.FormatInt(int64(subject), 10), nil
	case int32:
		return strconv.FormatInt(int64(subject), 10), nil
	case int64:
		return strconv.FormatInt(subject, 10), nil
	case uint:
		return strconv.FormatUint(uint64(subject), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(subject), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(subject), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(subject), 10), nil
	case uint64:
		return strconv.FormatUint(subject, 10), nil
	case fmt.Stringer:
		return subject.String(), nil
	default:
		return "", fmt.Errorf("unsupported current_user_id type %T", value)
	}
}

func cloneAuthorizationScope(scope map[string]string) map[string]string {
	if scope == nil {
		return nil
	}
	cloned := make(map[string]string, len(scope))
	for key, value := range scope {
		cloned[key] = value
	}
	return cloned
}

func permissionLogger(ctx *gin.Context) *slog.Logger {
	if ctx != nil {
		if value, exists := ctx.Get(runtimeContextKey); exists {
			if runtime, ok := value.(*Runtime); ok && runtime != nil && runtime.Logger != nil {
				return runtime.Logger
			}
		}
	}
	return legacyLogger()
}

func permissionStrictRuntime(ctx *gin.Context) bool {
	if ctx == nil {
		return true
	}
	value, exists := ctx.Get(runtimeContextKey)
	if !exists {
		return true
	}
	runtime, ok := value.(*Runtime)
	return ok && runtime != nil && runtime.Config != nil && runtime.Config.FrameworkStrict()
}

func requestContext(ctx *gin.Context) context.Context {
	if ctx != nil && ctx.Request != nil {
		return ctx.Request.Context()
	}
	return context.Background()
}

var _ Fairing = (*PermissionFairing)(nil)
