package file

// GroupPermission defines file-group RBAC levels.
type GroupPermission int

const (
	GroupPermissionRead GroupPermission = iota
	GroupPermissionWrite
	GroupPermissionManage
)
