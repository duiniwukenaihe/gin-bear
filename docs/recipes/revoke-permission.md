# Recipe: revoke a permission safely

Goal: remove access so the next authorization denies, on one instance and
then across instances.

Single instance (legacy interface):

```go
enforcer, _ := bear.NewCasbinEnforcer(adapter, nil)
// decision cache is off by default: the next Enforce sees this.
enforcer.RemoveGroupingPolicy("alice", "admin")
```

Single instance (online changes, concurrent-safe):

```go
authorizer, _ := bear.NewCasbinAuthorizer(adapter, nil)
authorizer.AddPolicy("admin", "/secret", "GET")
authorizer.AddGroupingPolicy("alice", "admin")
authorizer.RemoveGroupingPolicy("alice", "admin") // visible afterwards
```

Rules: only three string parameters per call (`sub, obj, act` for policies,
`user, role` for groupings); anything else is rejected before any write.
Non-empty `Scope` is rejected — tenant checks belong in a custom
`Authorizer` plus database conditions, never in the Casbin call.

Cluster: repeat `LoadPolicy` on every instance and collect success
confirmation; instances that never confirm must stop receiving protected
traffic. A lean local check of this boundary lives in
`TestCasbinAuthorizerMultiInstanceReload`.

Verify: `go test ./pkg/bear -run 'TestCasbinRevocation|TestCasbinAuthorizer' -count=1`.
