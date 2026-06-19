package bear

import (
	"strings"
)

// RouteFairing 存储路由和其绑定的 Fairings 的映射
type RouteFairing struct {
	Path     string
	Fairings []Fairing
}

// RouteTree 自定义路由树，用于存储路由级别的 Fairing
type RouteTree struct {
	trees map[string]*routeNode // key: HTTP method
}

// routeNode 路由树节点
type routeNode struct {
	path     string
	fairings []Fairing
	children map[string]*routeNode
	wildcard *routeNode // 处理 :param 这种参数路由
	catchAll *routeNode // 处理 *catchall 这种路由
}

func NewRouteTree() *RouteTree {
	return &RouteTree{
		trees: make(map[string]*routeNode),
	}
}

// addRoute 添加路由及其 Fairings
func (rt *RouteTree) addRoute(method, path string, fairings []Fairing) {
	if rt.trees == nil {
		rt.trees = make(map[string]*routeNode)
	}

	root, exists := rt.trees[method]
	if !exists {
		root = &routeNode{
			path:     "/",
			children: make(map[string]*routeNode),
		}
		rt.trees[method] = root
	}

	if path == "" || path == "/" {
		root.fairings = fairings
		return
	}

	// 移除前导斜杠进行插入
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := root

	for i, segment := range segments {
		if segment == "" {
			continue
		}

		// 检查是否是通配符路由
		if segment[0] == ':' {
			// 参数路由 :param
			if current.wildcard == nil {
				current.wildcard = &routeNode{
					path:     segment,
					children: make(map[string]*routeNode),
				}
			}
			current = current.wildcard
		} else if segment[0] == '*' {
			// 捕获所有路由 *catchall
			if current.catchAll == nil {
				current.catchAll = &routeNode{
					path: segment,
				}
			}
			current.catchAll.fairings = fairings
			return
		} else {
			// 静态路由
			if current.children == nil {
				current.children = make(map[string]*routeNode)
			}
			next, exists := current.children[segment]
			if !exists {
				next = &routeNode{
					path:     segment,
					children: make(map[string]*routeNode),
				}
				current.children[segment] = next
			}
			current = next
		}

		// 如果是最后一个 segment，设置 fairings
		if i == len(segments)-1 {
			current.fairings = fairings
		}
	}
}

// getRoute 获取路由的 Fairings
func (rt *RouteTree) getRoute(method, path string) []Fairing {
	root, exists := rt.trees[method]
	if !exists {
		return nil
	}

	// 处理根路径
	if path == "" || path == "/" {
		return root.fairings
	}

	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := root

	for i, segment := range segments {
		if segment == "" {
			continue
		}

		// 优先匹配静态路由
		if child, exists := current.children[segment]; exists {
			current = child
		} else if current.wildcard != nil {
			// 匹配参数路由
			current = current.wildcard
		} else if current.catchAll != nil {
			// 匹配捕获所有路由
			return current.catchAll.fairings
		} else {
			// 没有匹配
			return nil
		}

		// 如果是最后一个 segment，返回 fairings
		if i == len(segments)-1 {
			return current.fairings
		}
	}

	return current.fairings
}
