// Package domain 定义跨服务共享的领域类型：聚合根、枚举、错误码。
// APIServer 与 TransferServer 共用此包，避免类型定义重复。
package domain

import "errors"

// FileStatus 文件状态机，对应 files.status 枚举。
type FileStatus string

const (
	FileStatusInit       FileStatus = "init"
	FileStatusUploading  FileStatus = "uploading"
	FileStatusMerging    FileStatus = "merging"
	FileStatusValidating FileStatus = "validating"
	FileStatusCompleted  FileStatus = "completed"
	FileStatusFailed     FileStatus = "failed"
	FileStatusCancelled  FileStatus = "cancelled"
)

// UploadSessionStatus 上传会话状态，对应 upload_sessions.status 枚举。
type UploadSessionStatus string

const (
	UploadSessionActive   UploadSessionStatus = "active"
	UploadSessionComplete UploadSessionStatus = "completed"
	UploadSessionAborted  UploadSessionStatus = "aborted"
	UploadSessionExpired  UploadSessionStatus = "expired"
)

// ShareStatus 分享状态，对应 shares.status 枚举。
type ShareStatus string

const (
	ShareReady    ShareStatus = "ready"
	ShareActive   ShareStatus = "active"
	ShareExpired  ShareStatus = "expired"
	ShareCancelled ShareStatus = "cancelled"
)

// FileNode 对应 files 表的领域模型（用户视图的文件/文件夹节点）。
type FileNode struct {
	ID           int64   `db:"id" json:"id"`
	UserID       int64   `db:"user_id" json:"user_id"`
	ParentID     *int64  `db:"parent_id" json:"parent_id,omitempty"`
	Name         string  `db:"name" json:"name"`
	Size         int64   `db:"size" json:"size"`
	MimeType     string  `db:"mime_type" json:"mime_type"`
	IsFolder     bool    `db:"is_folder" json:"is_folder"`
	HashSHA256   *string `db:"hash_sha256" json:"hash_sha256,omitempty"`
	ChunkCount   int     `db:"chunk_count" json:"chunk_count"`
	Status       FileStatus `db:"status" json:"status"`
	StoragePath  *string `db:"storage_path" json:"storage_path,omitempty"`
	DeletedAt    *string `db:"deleted_at" json:"deleted_at,omitempty"`
	CreatedAt    string  `db:"created_at" json:"created_at"`
	UpdatedAt    string  `db:"updated_at" json:"updated_at"`
}

// User 对应 users 表。
type User struct {
	ID            int64  `db:"id" json:"id"`
	Username      string `db:"username" json:"username"`
	Email         string `db:"email" json:"email"`
	PasswordHash  string `db:"password_hash" json:"-"`
	StorageQuota  int64  `db:"storage_quota" json:"storage_quota"`
	UsedStorage   int64  `db:"used_storage" json:"used_storage"`
	Status        int16  `db:"status" json:"status"`
	IsAdmin       bool   `db:"is_admin" json:"is_admin"`
	CreatedAt     string `db:"created_at" json:"created_at"`
	UpdatedAt     string `db:"updated_at" json:"updated_at"`
}

// FileHash 对应 file_hashes 表（全局物理存储引用）。
type FileHash struct {
	HashSHA256  string `db:"hash_sha256" json:"hash_sha256"`
	StoragePath string `db:"storage_path" json:"storage_path"`
	Size        int64  `db:"size" json:"size"`
	RefCount    int    `db:"ref_count" json:"ref_count"`
	CreatedAt   string `db:"created_at" json:"created_at"`
}

// UploadSession 对应 upload_sessions 表。
type UploadSession struct {
	ID              string               `db:"id" json:"id"`
	UserID          int64                `db:"user_id" json:"user_id"`
	FileID          int64                `db:"file_id" json:"file_id"`
	HashSHA256      string               `db:"hash_sha256" json:"hash_sha256"`
	TotalSize       int64                `db:"total_size" json:"total_size"`
	ChunkSize       int                  `db:"chunk_size" json:"chunk_size"`
	TotalChunks     int                  `db:"total_chunks" json:"total_chunks"`
	UploadedChunks  []byte               `db:"uploaded_chunks" json:"uploaded_chunks,omitempty"`
	UploadID        string               `db:"upload_id" json:"upload_id"`
	Status          UploadSessionStatus  `db:"status" json:"status"`
	ExpiresAt       string               `db:"expires_at" json:"expires_at"`
	CreatedAt       string               `db:"created_at" json:"created_at"`
	UpdatedAt       string               `db:"updated_at" json:"updated_at"`
}

// Share 对应 shares 表。
type Share struct {
	ID           string     `db:"id" json:"id"`
	UserID       int64      `db:"user_id" json:"user_id"`
	FileID       int64      `db:"file_id" json:"file_id"`
	PasswordHash *string    `db:"password_hash" json:"-"`
	ExpiresAt    *string    `db:"expires_at" json:"expires_at,omitempty"`
	MaxAccess    *int       `db:"max_access" json:"max_access,omitempty"`
	AccessCount  int        `db:"access_count" json:"access_count"`
	Status       ShareStatus `db:"status" json:"status"`
	CreatedAt    string     `db:"created_at" json:"created_at"`
	UpdatedAt    string     `db:"updated_at" json:"updated_at"`
}

// ErrorCode 是统一业务错误码。
type ErrorCode string

const (
	CodeOK              ErrorCode = "0"
	CodeInvalidParam    ErrorCode = "40001"
	CodeUnauthorized    ErrorCode = "40101"
	CodeForbidden       ErrorCode = "40301"
	CodeNotFound        ErrorCode = "40401"
	CodeQuotaExceeded   ErrorCode = "41301"
	CodeConflict        ErrorCode = "40901"
	CodeRateLimited     ErrorCode = "42901"
	CodeInternal        ErrorCode = "50001"
)

// BizError 业务错误，携带错误码与 HTTP 状态。
type BizError struct {
	Code    ErrorCode
	Message string
	HTTP    int
	Err     error
}

func (e *BizError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *BizError) Unwrap() error { return e.Err }

// NewBizError 构造业务错误。
func NewBizError(code ErrorCode, http int, msg string, err error) *BizError {
	return &BizError{Code: code, HTTP: http, Message: msg, Err: err}
}

// 预定义错误。
var (
	ErrInvalidParam   = NewBizError(CodeInvalidParam, 400, "参数错误", nil)
	ErrUnauthorized   = NewBizError(CodeUnauthorized, 401, "未授权", nil)
	ErrForbidden      = NewBizError(CodeForbidden, 403, "权限拒绝", nil)
	ErrNotFound       = NewBizError(CodeNotFound, 404, "资源不存在", nil)
	ErrQuotaExceeded  = NewBizError(CodeQuotaExceeded, 413, "配额超限", nil)
	ErrConflict       = NewBizError(CodeConflict, 409, "资源冲突", nil)
	ErrRateLimited    = NewBizError(CodeRateLimited, 429, "请求过多", nil)
	ErrInternal       = NewBizError(CodeInternal, 500, "服务器内部错误", nil)
)

// AsBizError 从 error 提取 *BizError，否则包装为 ErrInternal。
func AsBizError(err error) *BizError {
	var be *BizError
	if errors.As(err, &be) {
		return be
	}
	return NewBizError(CodeInternal, 500, "服务器内部错误", err)
}
