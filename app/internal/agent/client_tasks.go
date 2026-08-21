package agent

import (
	"context"
	"errors"
	"net/url"

	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

var errNoContent = errors.New("server returned no content")

func (client *Client) ClaimTask(ctx context.Context, agentID string) (*task.Resource, error) {
	var response protocol.ClaimTaskResponse
	err := client.post(ctx, "/api/v1/agents/"+url.PathEscape(agentID)+"/tasks/claim", nil, &response, client.Credential())
	if errors.Is(err, errNoContent) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &response.Task, nil
}

func (client *Client) AcknowledgeTask(
	ctx context.Context,
	agentID, taskID, claimID string,
) (task.Resource, error) {
	var response task.Resource
	err := client.post(ctx,
		"/api/v1/agents/"+url.PathEscape(agentID)+"/tasks/"+url.PathEscape(taskID)+"/ack",
		protocol.AcknowledgeTaskRequest{ClaimID: claimID}, &response, client.Credential())
	return response, err
}

func (client *Client) SubmitTaskResult(
	ctx context.Context,
	agentID, taskID string,
	submission task.ResultSubmission,
) (task.Resource, error) {
	var response task.Resource
	err := client.post(ctx,
		"/api/v1/agents/"+url.PathEscape(agentID)+"/tasks/"+url.PathEscape(taskID)+"/result",
		submission, &response, client.Credential())
	return response, err
}
