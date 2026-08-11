# Task 4 Report

## 精确起点

- 工作区：`/Users/zhangpeng/data/work/gin-bear-v092-framework-hardening`
- 起点提交：`599b7e047af960118bf4f9fcc58e963e362ed8d8`
- 环境：`GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.25.12`

## 红灯证据

先新增严格 IoC 覆盖及同名静态 injector fixture，并运行：

```sh
go test ./pkg/bear -run 'TestStrictIOC|TestResolveE|TestApplyE|TestStaticInjectorKey' -count=1
```

初始结果为编译失败：缺少 `ResolveE`、`ApplyE`、`ErrBeanMissing`、`ErrBeanAmbiguous`、
`ErrBeanDuplicate`、`RuntimeStaticInjectorE` 与完整类型键函数。

全量包测试首次还稳定复现严格内置注入问题：`JWTUtil.Config` 被严格反射解析为独立
`*JWTConfig` Bean，返回 `bean missing`。补充 `TestStrictRuntimeStaticInjectorResolvesFromOwningContainer`
后单测同样红灯，证明应为内置组件提供 container-local E injector，不能回退到旧全局 API。

## 修改

- 增加可通过 `errors.Is` 识别的 `ErrBeanMissing`、`ErrBeanAmbiguous`、`ErrBeanDuplicate`，以及
  `ResolveE` 和 `ApplyE` 的不泄露 Bean 值的类型/字段诊断。
- 严格解析优先精确显式接口绑定；无显式绑定时只接受唯一具体实现，多实现返回歧义。
- 严格容器拒绝同一具体类型的不同实例，允许相同实例幂等；旧 `Set`/`Beans` 继续覆盖，但会
  记录冲突，严格 `ApplyAll` 在初始化和路由构建前返回 duplicate 错误。
- 新增全包路径静态 E injector 注册表；`ApplyE` 只使用 E injector 或本容器反射，旧短名
  injector 仍仅服务 legacy `Apply`。内置 `JWTUtil` 使用当前容器的 `SysConfig` 完成 E 注入。
- 新增 `BeansE`、`AddModuleE`、`MountE`，注册失败返回错误且不发布失败的 Bean、Module 或 Mount
  metadata；生命周期的关闭注册错误仍原样透传。
- 新增 `ioc_strict_test.go` 与两个只定义同名类型的 fixture 包，并补充严格运行时 injector
  归属测试。

## 绿灯证据

```sh
go test ./pkg/bear -run 'TestStrictIOC|TestResolveE|TestApplyE|TestStaticInjectorKey' -count=1
go test ./pkg/bear -run 'Test.*(IOC|Inject|Bean|Container)' -count=1
go test ./pkg/bear -count=1
```

三档均通过；最终包级全量测试耗时约 9.9 秒。

## 范围与发布

- 未修改 Fairing，也未实现 Task 5/6 的 `BuildE` 或生成器迁移。
- 本提交尚未推送、未打 tag、未合并。

## 残余风险

`BeansE`/`AddModuleE` 的可变参数注册按现有生命周期逐项提交；单个后续 Bean 被拒绝时，先前已
成功注册的 Bean 保持可用，Module/Mount metadata 不会发布。跨整个参数列表的全事务回滚不在本
任务既有容器生命周期模型内。
