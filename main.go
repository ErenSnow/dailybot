package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/robfig/cron/v3"
)

var (
	ChatId    = os.Getenv("ChatId")
	AppId     = os.Getenv("AppId")
	AppSecret = os.Getenv("AppSecret")

	// 未发言提醒时忽略的 open_id，不 @ 这些人
	noSendRemindIgnoreOpenIds = map[string]bool{
		"ou_fd169f0d68d664cc29829731e48eadd0": true,
		"ou_0722adfa262773676183b7d14840d70a": true,
		"ou_ce904ac7b7dda5793e95b20b840d894a": true,
	}
)

func main() {
	c := cron.New(cron.WithLocation(time.Local))
	_, _ = c.AddFunc("0 11 * * *", runNoSendRemindTask) // 每天 11:00 执行：提醒未发言成员
	_, _ = c.AddFunc("30 11 * * *", runDailyTask)       // 每天 11:30 执行晨会汇总
	c.Start()
	fmt.Println("定时任务已启动：11:00 未发言提醒，11:30 晨会汇总")
	select {} // 阻塞保持进程运行
}

// runNoSendRemindTask 每天 11:00：统计 9:00~11:00 未发言成员，并 @ 提醒
func runNoSendRemindTask() {
	client := lark.NewClient(AppId, AppSecret)
	members := getAllMembers(client)
	if len(members) == 0 {
		return
	}

	now := time.Now()
	startTime := strconv.FormatInt(time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location()).Unix(), 10)
	endTime := strconv.FormatInt(time.Date(now.Year(), now.Month(), now.Day(), 11, 0, 0, 0, now.Location()).Unix(), 10)
	req := larkim.NewListMessageReqBuilder().
		ContainerIdType(`chat`).
		ContainerId(ChatId).
		StartTime(startTime).
		EndTime(endTime).
		SortType(`ByCreateTimeAsc`).
		PageSize(50).
		Build()

	resp, err := client.Im.Message.List(context.Background(), req)
	if err != nil {
		fmt.Println("runNoSendRemindTask List err:", err)
		return
	}
	if !resp.Success() {
		fmt.Printf("runNoSendRemindTask List fail: %s\n", larkcore.Prettify(resp.CodeError))
		return
	}

	// 统计在 9:00~11:00 发过言的 open_id
	sentOpenIds := make(map[string]bool)
	if resp.Data != nil && resp.Data.Items != nil {
		for _, msg := range resp.Data.Items {
			if msg != nil && msg.Sender != nil && msg.Sender.Id != nil {
				sentOpenIds[*msg.Sender.Id] = true
			}
		}
	}

	// 未发言成员：在群成员里但不在发言集合中，且不在忽略名单中
	var noSendOpenIds []string
	var noSendNames []string
	for openId, name := range members {
		if noSendRemindIgnoreOpenIds[openId] {
			continue
		}

		if !sentOpenIds[openId] {
			noSendOpenIds = append(noSendOpenIds, openId)
			noSendNames = append(noSendNames, name)
		}
	}

	if len(noSendOpenIds) == 0 {
		fmt.Println("11:00 提醒：全员已在晨会时段发言，无需 @")
		return
	}

	sendMessageWithMentions(client, noSendOpenIds, noSendNames)
}

// runDailyTask 原主流程：拉取 9:00~11:00 群消息，整理后发回群
func runDailyTask() {
	client := lark.NewClient(AppId, AppSecret)

	members := getAllMembers(client)
	fmt.Println("成员列表:", members)

	now := time.Now()
	startTime := strconv.FormatInt(time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location()).Unix(), 10)
	endTime := strconv.FormatInt(time.Date(now.Year(), now.Month(), now.Day(), 11, 0, 0, 0, now.Location()).Unix(), 10)
	req := larkim.NewListMessageReqBuilder().
		ContainerIdType(`chat`).
		ContainerId(ChatId).
		StartTime(startTime).
		EndTime(endTime).
		SortType(`ByCreateTimeAsc`).
		PageSize(50).
		Build()

	resp, err := client.Im.Message.List(context.Background(), req)
	if err != nil {
		fmt.Println(err)
		return
	}
	if !resp.Success() {
		fmt.Printf("logId: %s, error response: \n%s", resp.RequestId(), larkcore.Prettify(resp.CodeError))
		return
	}

	if resp.Data != nil && resp.Data.Items != nil && len(resp.Data.Items) > 0 {
		var combined strings.Builder
		currentTime := time.Now()
		formattedTime := currentTime.Format(time.DateOnly)
		combined.WriteString(fmt.Sprintf("%s 晨会记录：\n", formattedTime))
		combined.WriteString(fmt.Sprintf("会议应到 %d 人，实到 ? 人  请假：?\n\n", len(members)))

		for _, msg := range resp.Data.Items {
			senderName := getSenderName(msg, members)
			content := getMessageContent(msg)
			combined.WriteString(fmt.Sprintf("%s\n%s\n\n", senderName, content))
		}
		if combined.Len() > 0 {
			sendMessage(client, strings.TrimSuffix(combined.String(), "\n"))
		}
	}
}

// sendMessage 将整理好的消息以文本形式发送到当前群
func sendMessage(client *lark.Client, msg string) {
	contentBytes, _ := json.Marshal(map[string]string{"text": msg})
	content := string(contentBytes)

	fmt.Println(content)

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(`chat_id`).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(ChatId).
			MsgType(`text`).
			Content(content).
			Uuid(fmt.Sprintf("msg-%d", time.Now().UnixNano())).
			Build()).
		Build()

	resp, err := client.Im.Message.Create(context.Background(), req)
	if err != nil {
		fmt.Println("sendMessage err:", err)
		return
	}
	if !resp.Success() {
		fmt.Printf("sendMessage fail, logId: %s, %s\n", resp.RequestId(), larkcore.Prettify(resp.CodeError))
		return
	}
	fmt.Println("已发送:", msg)
}

// sendMessageWithMentions 发送一条带 @ 提及的文本消息到当前群
// content 格式：{"text":"<at user_id=\"ou_xxx\">姓名</at> 文案"}
func sendMessageWithMentions(client *lark.Client, openIds, names []string) {
	if len(openIds) == 0 || len(openIds) != len(names) {
		return
	}
	var atParts []string
	for i := range openIds {
		atParts = append(atParts, fmt.Sprintf(`<at user_id="%s">%s</at>`, openIds[i], names[i]))
	}
	text := "【日报提醒】" + strings.Join(atParts, " ") + "，麻烦同步一下日报，谢谢。"
	contentBytes, _ := json.Marshal(map[string]string{"text": text})
	content := string(contentBytes)
	fmt.Println(content)

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(`chat_id`).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(ChatId).
			MsgType(`text`).
			Content(content).
			Uuid(fmt.Sprintf("mention-%d", time.Now().UnixNano())).
			Build()).
		Build()

	resp, err := client.Im.Message.Create(context.Background(), req)
	if err != nil {
		fmt.Println("sendMessageWithMentions err:", err)
		return
	}
	if !resp.Success() {
		fmt.Printf("sendMessageWithMentions fail: %s\n", larkcore.Prettify(resp.CodeError))
		return
	}
	fmt.Println("已发送 @ 提醒，未发言人数:", len(openIds))
}

// getSenderName 从消息和成员表中解析发送者名称（open_id 与成员表一致时）
func getSenderName(msg *larkim.Message, members map[string]string) string {
	if msg == nil || msg.Sender == nil || msg.Sender.Id == nil {
		return "未知"
	}
	id := *msg.Sender.Id
	if name, ok := members[id]; ok {
		return name
	}
	return id // 不在成员表时显示 id
}

// getMessageContent 提取可读的消息内容；文本类型解析 JSON 中的 text，其它类型返回简要描述
func getMessageContent(msg *larkim.Message) string {
	if msg == nil || msg.Body == nil || msg.Body.Content == nil {
		return ""
	}
	content := *msg.Body.Content
	if content == "" {
		return ""
	}
	msgType := "text"
	if msg.MsgType != nil {
		msgType = *msg.MsgType
	}
	if msgType == "text" {
		var parsed struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &parsed); err == nil && parsed.Text != "" {
			return parsed.Text
		}
	}
	return content
}

// getAllMembers 获取群聊所有成员，返回 map[MemberId]Name
func getAllMembers(client *lark.Client) map[string]string {
	result := make(map[string]string)
	pageToken := ""
	for {
		builder := larkim.NewGetChatMembersReqBuilder().
			ChatId(ChatId).
			MemberIdType(`open_id`).
			PageSize(50)
		if pageToken != "" {
			builder = builder.PageToken(pageToken)
		}
		req := builder.Build()

		resp, err := client.Im.ChatMembers.Get(context.Background(), req)
		if err != nil {
			fmt.Println(err)
			return nil
		}
		if !resp.Success() {
			fmt.Printf("logId: %s, error response: \n%s", resp.RequestId(), larkcore.Prettify(resp.CodeError))
			return nil
		}

		data := resp.Data
		if data == nil || data.Items == nil {
			break
		}
		for _, m := range data.Items {
			if m != nil && m.MemberId != nil && m.Name != nil {
				result[*m.MemberId] = *m.Name
			}
		}
		if data.HasMore == nil || !*data.HasMore || data.PageToken == nil {
			break
		}
		pageToken = *data.PageToken
	}
	return result
}
