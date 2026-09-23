package agent

import (
	"fmt"
	"strings"
)

// Routing binds task capabilities to a provider, budget, and fallback
// allowlist. Fallbacks are explicit and recorded; the runner never silently
// swaps vendors or degrades capability.
type Routing struct {
	// Task is the capability name, e.g. "readonly-query".
	Task string
	// Provider names the serving vendor, e.g. "fake" or "openai".
	Provider string
	Budget   Budget
	// Fallbacks lists providers the router may try after a primary failure,
	// in order. Empty means no fallback.
	Fallbacks []string
}

// Router resolves task routing and builds runners from registered models.
type Router struct {
	routes map[string]Routing
	models map[string]ChatModel
}

// NewRouter validates routing entries: every route needs a task, a provider
// model, and fallbacks that also exist. Unknown fallback providers are
// rejected rather than silently dropped.
func NewRouter(routes []Routing, models map[string]ChatModel) (*Router, error) {
	router := &Router{routes: map[string]Routing{}, models: map[string]ChatModel{}}
	for name, model := range models {
		if model == nil {
			return nil, fmt.Errorf("router model %q is nil", name)
		}
		router.models[name] = model
	}
	for _, route := range routes {
		if strings.TrimSpace(route.Task) == "" {
			return nil, fmt.Errorf("router route needs a task")
		}
		if _, ok := router.models[route.Provider]; !ok {
			return nil, fmt.Errorf("route %q provider %q is not registered", route.Task, route.Provider)
		}
		for _, fallback := range route.Fallbacks {
			if _, ok := router.models[fallback]; !ok {
				return nil, fmt.Errorf("route %q fallback %q is not registered", route.Task, fallback)
			}
		}
		router.routes[route.Task] = route
	}
	return router, nil
}

// Resolve returns the routing for a task.
func (r *Router) Resolve(task string) (Routing, error) {
	route, ok := r.routes[task]
	if !ok {
		return Routing{}, fmt.Errorf("no routing for task %q", task)
	}
	return route, nil
}

// RunnerFor builds the primary runner for a task with the given tools.
func (r *Router) RunnerFor(task string, tools *Registry) (*Runner, error) {
	route, err := r.Resolve(task)
	if err != nil {
		return nil, err
	}
	return &Runner{Model: r.models[route.Provider], Tools: tools, Budget: route.Budget, Provider: route.Provider}, nil
}

// FallbacksFor lists the fallback runners in order for callers that retry
// explicitly. Each retry is a new budgeted run, recorded separately.
func (r *Router) FallbacksFor(task string, tools *Registry) ([]*Runner, error) {
	route, err := r.Resolve(task)
	if err != nil {
		return nil, err
	}
	var runners []*Runner
	for _, provider := range route.Fallbacks {
		runners = append(runners, &Runner{Model: r.models[provider], Tools: tools, Budget: route.Budget, Provider: provider})
	}
	return runners, nil
}
