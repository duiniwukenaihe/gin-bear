package bear

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/gin-gonic/gin"
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

type compiledHandler func(*gin.Context) (any, error)

func compileHandler(handler any) (compiledHandler, error) {
	value := reflect.ValueOf(handler)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return nil, fmt.Errorf("handler must be a function, got %T", handler)
	}
	plan, err := compileArguments(value.Type())
	if err != nil {
		return nil, err
	}
	if err := validateHandlerResults(value.Type()); err != nil {
		return nil, err
	}
	return func(ctx *gin.Context) (any, error) {
		args, err := plan.Bind(ctx)
		if err != nil {
			message := "Invalid request"
			var pathError *pathBindingError
			if errors.As(err, &pathError) {
				message = "Invalid path parameter"
			}
			return nil, NewStatusError(400, 400, "error_invalid_params", err).WithMsg(message)
		}
		return decodeHandlerResults(value.Call(args))
	}, nil
}

func validateHandlerResults(handlerType reflect.Type) error {
	switch handlerType.NumOut() {
	case 0:
		return nil
	case 1:
		return nil
	case 2:
		if !handlerType.Out(1).Implements(errorType) {
			return fmt.Errorf("handler %s second result must implement error", handlerType)
		}
		if handlerType.Out(0).Implements(errorType) {
			return fmt.Errorf("handler %s first result must be a response value, not an error", handlerType)
		}
		return nil
	default:
		return fmt.Errorf("handler %s must return at most a response value and an error", handlerType)
	}
}

func decodeHandlerResults(results []reflect.Value) (any, error) {
	switch len(results) {
	case 0:
		return nil, nil
	case 1:
		if results[0].Type().Implements(errorType) {
			if isNilReflectValue(results[0]) {
				return nil, nil
			}
			return nil, results[0].Interface().(error)
		}
		return results[0].Interface(), nil
	case 2:
		if !isNilReflectValue(results[1]) {
			return nil, results[1].Interface().(error)
		}
		return results[0].Interface(), nil
	default:
		panic("handler results were not validated")
	}
}

func isNilReflectValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func opaqueGinHandler(handler any) (gin.HandlerFunc, bool) {
	switch typed := handler.(type) {
	case gin.HandlerFunc:
		return typed, true
	case func(*gin.Context):
		return gin.HandlerFunc(typed), true
	default:
		return nil, false
	}
}
