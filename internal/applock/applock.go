// Package applock: 单实例守卫。同一 state 目录只允许一个采集/控制台
// 进程——双实例并发写台账（last-writer-wins）与 SQLite 锁冲突已在
// 运维中实际发生过。锁文件 <dir>/.lock 记录持有者 pid，进程退出即释放。
package applock
