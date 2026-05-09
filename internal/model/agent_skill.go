package model

import (
	"gorm.io/datatypes"
)

// AgentSkill Agent 声明的能力单元，对应 A2A AgentCard.skills[]
type AgentSkill struct {
	ID          int64          `gorm:"primaryKey;autoIncrement"                                    json:"id"`
	AgentID     string         `gorm:"column:agent_id;type:varchar(128);index;not null"            json:"agent_id"`
	SkillID     string         `gorm:"column:skill_id;type:varchar(128);not null"                  json:"skill_id"`
	Name        string         `gorm:"column:name;type:varchar(128);not null"                      json:"name"`
	Description string         `gorm:"column:description;type:text"                                json:"description"`
	Tags        datatypes.JSON `gorm:"column:tags;type:json"                                       json:"tags"`        // []string
	InputModes  datatypes.JSON `gorm:"column:input_modes;type:json"                                json:"input_modes"` // []string
	OutputModes datatypes.JSON `gorm:"column:output_modes;type:json"                               json:"output_modes"`
}

func (AgentSkill) TableName() string { return "agent_skills" }
