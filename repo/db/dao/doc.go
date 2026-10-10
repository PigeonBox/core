// Package dao 数据访问对象层：各域模型的 GORM 持久化实现。
//
// # DB 注入约定（去全局化第一步）
//
// 全部 Repository 构造函数接受可选变参 *gorm.DB：
//
//	repo := dao.NewFileCodeRepository()        // 缺省：走全局 db.GetDB()（兼容历史行为）
//	repo := dao.NewFileCodeRepository(gormDB)  // 注入：全部读写落在 gormDB 实例
//
// 规则：
//   - 注入 nil 等价于不注入（db() 回退全局 db.GetDB()）；
//   - 注入实例后 Repository 不再读取任何全局 DB 状态；
//   - 动机：解锁不依赖全局 DB 状态的隔离测试（:memory: 库、并行子测试），
//     以及嵌入式/多实例场景（同进程多套库）；
//   - 新代码优先显式注入，无参构造仅为兼容既有消费方保留。
//
// 容错说明：各 db() 方法带 nil receiver 守卫（r != nil）。历史上
// Repository 是零尺寸空 struct，方法体不解引用 receiver，个别消费方
// 依赖"懒接线未就绪时以 nil *Repository 直调也能跑"的行为；加 conn
// 字段后为不破坏该兼容性，守卫保留 nil receiver 回退全局的语义。
package dao
