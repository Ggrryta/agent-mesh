package jwtclaims

import "github.com/golang-jwt/jwt/v5"

// Claims JWT payload。授权范围由 agent_permissions 表决定，claims 只承载身份。
type Claims struct {
	AppID string `json:"app_id"`
	jwt.RegisteredClaims
}
