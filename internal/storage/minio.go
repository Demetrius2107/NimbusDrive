// Package storage 封装 MinIO 客户端，提供分块上传/下载/直传能力。
// 仅 TransferServer 直接使用；APIServer 在秒传判定时也可能读取对象元信息。
package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIO 封装 minio 客户端与 bucket 信息。
// Client 用于高层 API（PutObject/GetObject），Core 用于底层分块上传控制。
// PresignClient 用客户端可达的公网端点构造，仅用于签发预签名下载 URL；
// 内部上传/下载走 Client（内网端点）。两者共享同一 bucket 与凭证。
type MinIO struct {
	Client       *minio.Client
	Core         *minio.Core
	PresignClient *minio.Client
	Bucket       string
}

// New 创建 MinIO 客户端。MVP 默认单桶，按 hash 前缀分目录存对象。
// publicEndpoint 非空时构造独立的 PresignClient；为空则 PresignClient 复用 Client。
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket, region string, useSSL bool, publicEndpoint string, publicUseSSL bool) (*MinIO, error) {
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	// 确保 bucket 存在。
	exists, err := cli.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			return nil, fmt.Errorf("make bucket %s: %w", bucket, err)
		}
	}

	// 预签名客户端：用公网端点签发客户端可达的 URL。公网空则复用内网 client。
	presignCli := cli
	if publicEndpoint != "" {
		presignCli, err = minio.New(publicEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
			Secure: publicUseSSL,
			Region: region,
		})
		if err != nil {
			return nil, fmt.Errorf("create presign minio client: %w", err)
		}
	}

	return &MinIO{
		Client:        cli,
		Core:          &minio.Core{Client: cli},
		PresignClient: presignCli,
		Bucket:        bucket,
	}, nil
}

// ObjectKey 按设计文档规范生成对象 key：blobs/{hash[:2]}/{hash[2:4]}/{hash}
func ObjectKey(hash string) string {
	if len(hash) < 4 {
		return "blobs/" + hash
	}
	return fmt.Sprintf("blobs/%s/%s/%s", hash[:2], hash[2:4], hash)
}

// --- 分块上传（Multipart Upload，通过 Core 暴露底层 S3 API） ---

// CreateMultipartUpload 初始化分块上传，返回 uploadID。
func (m *MinIO) CreateMultipartUpload(ctx context.Context, objectKey string) (string, error) {
	uploadID, err := m.Core.NewMultipartUpload(ctx, m.Bucket, objectKey, minio.PutObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("create multipart upload: %w", err)
	}
	return uploadID, nil
}

// UploadPart 上传单个分块，返回 ETag。
// partNumber 从 1 开始（S3 规范），调用方需 chunk_index + 1。
func (m *MinIO) UploadPart(ctx context.Context, objectKey, uploadID string, partNumber int, reader io.Reader, size int64) (string, error) {
	obj, err := m.Core.PutObjectPart(ctx, m.Bucket, objectKey, uploadID, partNumber, reader, size, minio.PutObjectPartOptions{})
	if err != nil {
		return "", fmt.Errorf("upload part %d: %w", partNumber, err)
	}
	return obj.ETag, nil
}

// ListParts 列出已上传的分块（complete 时用，避免依赖本地 upload_chunks 表）。
func (m *MinIO) ListParts(ctx context.Context, objectKey, uploadID string) ([]minio.ObjectPart, error) {
	result, err := m.Core.ListObjectParts(ctx, m.Bucket, objectKey, uploadID, 0, 10000)
	if err != nil {
		return nil, fmt.Errorf("list parts: %w", err)
	}
	return result.ObjectParts, nil
}

// CompleteMultipartUpload 合并所有分块为最终对象。
func (m *MinIO) CompleteMultipartUpload(ctx context.Context, objectKey, uploadID string, parts []minio.CompletePart) error {
	_, err := m.Core.CompleteMultipartUpload(ctx, m.Bucket, objectKey, uploadID, parts, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	return nil
}

// AbortMultipartUpload 取消分块上传，清理已上传的 Part。
func (m *MinIO) AbortMultipartUpload(ctx context.Context, objectKey, uploadID string) error {
	err := m.Core.AbortMultipartUpload(ctx, m.Bucket, objectKey, uploadID)
	if err != nil {
		return fmt.Errorf("abort multipart upload: %w", err)
	}
	return nil
}

// --- 整体上传/下载（高层 API） ---

// PutObject 整体上传一个对象（秒传场景：内容来自已有对象，此处用于小文件直传）。
func (m *MinIO) PutObject(ctx context.Context, objectKey string, reader io.Reader, size int64) error {
	_, err := m.Client.PutObject(ctx, m.Bucket, objectKey, reader, size, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

// GetObject 获取对象 Reader（下载用，支持 range）。
func (m *MinIO) GetObject(ctx context.Context, objectKey string, opts minio.GetObjectOptions) (*minio.Object, error) {
	obj, err := m.Client.GetObject(ctx, m.Bucket, objectKey, opts)
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	return obj, nil
}

// StatObject 获取对象元信息（大小等）。
func (m *MinIO) StatObject(ctx context.Context, objectKey string) (minio.ObjectInfo, error) {
	info, err := m.Client.StatObject(ctx, m.Bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return minio.ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	return info, nil
}

// RemoveObject 删除对象（ref_count=0 GC 时用）。
func (m *MinIO) RemoveObject(ctx context.Context, objectKey string) error {
	err := m.Client.RemoveObject(ctx, m.Bucket, objectKey, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("remove object: %w", err)
	}
	return nil
}

// --- 预签名下载 ---

// PresignedDownloadURL 签发一次性直连 MinIO 的下载 URL。
// 通过 response-content-type / response-content-disposition 查询参数覆盖响应头，
// 让浏览器以正确文件名与 MIME 保存，无需服务端代理字节流。
// 预签名 URL 的签名覆盖 query 参数，不覆盖 Range 头——客户端可对同一 URL
// 发多次 Range 请求做分块下载，无需每块单独签。
func (m *MinIO) PresignedDownloadURL(ctx context.Context, storagePath string, expire int, filename, mimeType string) (string, error) {
	reqParams := url.Values{}
	if mimeType != "" {
		reqParams.Set("response-content-type", mimeType)
	}
	if filename != "" {
		reqParams.Set("response-content-disposition", BuildContentDisposition(filename))
	}
	u, err := m.PresignClient.PresignedGetObject(ctx, m.Bucket, storagePath, toDuration(expire), reqParams)
	if err != nil {
		return "", fmt.Errorf("presigned get: %w", err)
	}
	return u.String(), nil
}

// toDuration 把秒转 time.Duration，<=0 时回退到 1 小时。
func toDuration(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

// BuildContentDisposition 构造 Content-Disposition 头。
// 文件名纯 ASCII 用 filename="..."；含非 ASCII 用 RFC 5987 的 filename*=UTF-8''...
// 预签名 URL 的 response-content-disposition 与流式下载响应头共用此构造。
func BuildContentDisposition(filename string) string {
	if IsASCII(filename) {
		return fmt.Sprintf(`attachment; filename="%s"`, filename)
	}
	encoded := url.PathEscape(filename)
	return fmt.Sprintf(`attachment; filename*=UTF-8''%s`, encoded)
}

// IsASCII 判断字符串是否全部为 ASCII 字符。
func IsASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}
