// Package storage 封装 MinIO 客户端，提供分块上传/下载/直传能力。
// 仅 TransferServer 直接使用；APIServer 在秒传判定时也可能读取对象元信息。
package storage

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIO 封装 minio 客户端与 bucket 信息。
type MinIO struct {
	Client *minio.Client
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
	return &MinIO{Client: cli, Bucket: bucket}, nil
}

// ObjectKey 按设计文档规范生成对象 key：blobs/{hash[:2]}/{hash[2:4]}/{hash}
func ObjectKey(hash string) string {
	if len(hash) < 4 {
		return "blobs/" + hash
	}
	return fmt.Sprintf("blobs/%s/%s/%s", hash[:2], hash[2:4], hash)
}
