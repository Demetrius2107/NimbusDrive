// Package storage 封装 MinIO 客户端，提供分块上传/下载/直传能力。
// 仅 TransferServer 直接使用；APIServer 在秒传判定时也可能读取对象元信息。
package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIO 封装 minio 客户端与 bucket 信息。
// Client 用于高层 API（PutObject/GetObject），Core 用于底层分块上传控制。
type MinIO struct {
	Client *minio.Client
	Core   *minio.Core
	Bucket string
}

// New 创建 MinIO 客户端。MVP 默认单桶，按 hash 前缀分目录存对象。
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket, region string, useSSL bool) (*MinIO, error) {
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
	return &MinIO{
		Client: cli,
		Core:   &minio.Core{Client: cli},
		Bucket: bucket,
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
