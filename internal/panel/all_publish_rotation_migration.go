package panel

// 迁移 42：许可保存全部发布成员的记录身份、绑定代次及保留数决定。
// 既有已下发许可继续收尾；旧的未下发许可须在新屏障下重新申请。
const allPublishRotationMigration = `CREATE TABLE rotation_member_barriers (
 action_id TEXT NOT NULL REFERENCES rotation_actions(id) ON DELETE CASCADE,
 group_id TEXT NOT NULL, desired_version INTEGER NOT NULL,
 minimum_generation INTEGER NOT NULL, minimum_kept INTEGER NOT NULL,
 managed_record_id TEXT NOT NULL, binding_generation INTEGER NOT NULL,
 enabled INTEGER NOT NULL,
 PRIMARY KEY(action_id,group_id)
);`
