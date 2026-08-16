// Package auth 提供 JWT 签发与解析。
// 与 middleware 分离：本包只处理 token 逻辑，框架适配（Gin/Hertz）在 middleware 层。
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 是 NimbusDrive 的 JWT 声明。
type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"usr"`
	IsAdmin  bool   `json:"adm,omitempty"`
	jwt.RegisteredClaims
}

// JWTManager 管理 token 签发与解析。
type JWTManager struct {
	secret       []byte
	accessExpMin int
	refreshExpDay int
	issuer       string
}

// New 创建 JWTManager。
func New(secret string, accessExpMin, refreshExpDay int, issuer string) *JWTManager {
	return &JWTManager{
		secret:        []byte(secret),
		accessExpMin:  accessExpMin,
		refreshExpDay: refreshExpDay,
		issuer:        issuer,
	}
}

// IssueAccessToken 签发访问令牌。
func (m *JWTManager) IssueAccessToken(userID int64, username string, isAdmin bool) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		IsAdmin:  isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(m.accessExpMin) * time.Minute)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// IssueRefreshToken 签发刷新令牌（生命周期更长，MVP 暂不暴露刷新接口，预留）。
func (m *JWTManager) IssueRefreshToken(userID int64, username string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.AddDate(0, 0, m.refreshExpDay)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

// Parse 解析并校验 token。返回 Claims 或错误。
func (m *JWTManager) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// ExtractBearer 从 "Bearer <token>" 头中提取 token。
func ExtractBearer(authHeader string) (string, bool) {
	const prefix = "Bearer "
	if len(authHeader) <= len(prefix) || authHeader[:len(prefix)] != prefix {
		return "", false
	}
	return authHeader[len(prefix):], true
}
