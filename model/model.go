package model

import "douyinlive/database"

type Comment struct {
	LiveId  int    `json:"live_id"`
	Content string `json:"content"`
}

func InsertComments(liveId int, content string) {
	comment := Comment{
		LiveId:  liveId,
		Content: content,
	}
	database.DB.Table("comments").Create(&comment)
}
