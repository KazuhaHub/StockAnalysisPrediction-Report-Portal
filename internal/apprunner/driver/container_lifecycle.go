package driver

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// These private adapters observe exact ownership and effective policy before
// every action and after each potentially effectful request. They return current
// state observations, not journal receipts, app health or execution authority.
// No caller-selected command, signal, timeout, endpoint or force mode is accepted.
func startCandidateContainerHTTP(ctx context.Context, client *http.Client, nativeID string, plan containerPlan) (string, error) {
	state, err := inspectCandidateContainerHTTP(ctx, client, nativeID, plan)
	if err != nil {
		return "", err
	}
	if state == "running" {
		return state, nil
	}
	// An exited process requires crash-loop/restart admission, not an implicit
	// retry of the initial start request under its old command identity.
	if state != "created" {
		return "", ErrContainerConflict
	}
	if err := candidateLifecyclePOST(ctx, client, nativeID, "start"); err != nil {
		return "", err
	}
	observed, err := inspectCandidateContainerHTTP(ctx, client, nativeID, plan)
	if err != nil {
		return "", errors.Join(ErrContainerUncertain, err)
	}
	if observed != "running" {
		return "", ErrContainerUncertain
	}
	return observed, nil
}
func stopCandidateContainerHTTP(ctx context.Context, client *http.Client, nativeID string, plan containerPlan) (string, error) {
	state, err := inspectCandidateContainerHTTP(ctx, client, nativeID, plan)
	if err != nil {
		return "", err
	}
	if state == "created" || state == "exited" {
		return state, nil
	}
	if state != "running" {
		return "", ErrContainerConflict
	}
	if err := candidateLifecyclePOST(ctx, client, nativeID, "stop"); err != nil {
		return "", err
	}
	observed, err := inspectCandidateContainerHTTP(ctx, client, nativeID, plan)
	if err != nil {
		return "", errors.Join(ErrContainerUncertain, err)
	}
	if observed != "exited" {
		return "", ErrContainerUncertain
	}
	return observed, nil
}
func candidateLifecyclePOST(ctx context.Context, client *http.Client, nativeID, action string) error {
	if !containerSHA.MatchString(nativeID) || (action != "start" && action != "stop") {
		return ErrInvalidResponse
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := "/v" + APIVersion + "/containers/" + nativeID + "/" + action
	if action == "stop" {
		path += "?signal=SIGTERM&t=10"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://app-runner-docker.invalid"+path, nil)
	if err != nil {
		return ErrInvalidResponse
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotModified {
		return ErrContainerUncertain
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrContainerUncertain, err)
	}
	return nil
}
