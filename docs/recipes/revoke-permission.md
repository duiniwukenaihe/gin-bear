# Recipe: revoke a permission safely

Goal: remove access so the next authorization denies across instances.

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

Cluster: with a persistent adapter, every `Authorize` reloads the policy
before deciding. A committed revocation on one instance therefore reaches
the next authorization on every instance. Reload errors fail closed and
require an explicit successful `LoadPolicy` to recover. Plan for one database
policy read per authorization and test capacity at the expected traffic rate.
`TestCasbinAuthorizerMultiInstanceReload` covers the cross-instance boundary.

Verify: `go test ./pkg/bear -run 'TestCasbinRevocation|TestCasbinAuthorizer' -count=1`.
