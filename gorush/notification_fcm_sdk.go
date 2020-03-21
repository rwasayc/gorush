package gorush

import (
	"context"
	"errors"
	firebase "firebase.google.com/go"
	"firebase.google.com/go/messaging"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"
)

// InitFCMSDKClient use for initialize FCM SDK Client.
func InitFCMSDKClient(ctx context.Context, config *firebase.Config, opts ...option.ClientOption) (*messaging.Client, error) {
	if config == nil {
		return nil, errors.New("Missing Android Config")
	}

	if config.ProjectID != PushConf.Android.ProjectID {
		app, err := firebase.NewApp(ctx, config, opts...)
		if err != nil {
			return nil, err
		}
		return app.Messaging(ctx)
	}

	if FCMSDKClient == nil {
		app, err := firebase.NewApp(ctx, config, opts...)
		if err != nil {
			return nil, err
		}
		FCMSDKClient, err = app.Messaging(ctx)
		if err != nil {
			return nil, err
		}
	}

	return FCMSDKClient, nil
}

// GetAndroidNotification use for define Android notification.
// HTTP Connection Server Reference for Android
func GetAndroidFCMSDKNotification(req PushNotification) []*messaging.MulticastMessage {
	notification := messaging.MulticastMessage{
		Android: &messaging.AndroidConfig{},
	}

	if len(req.Priority) > 0 && req.Priority == "high" {
		notification.Android.Priority = "high"
	}

	if len(req.CollapseKey) > 0 {
		notification.Android.CollapseKey = req.CollapseKey
	}

	if req.TimeToLive != nil && (*req.TimeToLive) > 0 {
		s := time.Duration((*req.TimeToLive)) * time.Second
		notification.Android.TTL = &s
	}

	// Add another field
	if len(req.Data) > 0 {
		notification.Data = make(map[string]string)
		for k, v := range req.Data {
			notification.Data[k] = fmt.Sprintf("%v", v)
		}
	}

	n := &messaging.AndroidNotification{}
	isNotificationSet := false
	if req.MNotification != nil {
		isNotificationSet = true
		n = req.MNotification
	}

	if len(req.Message) > 0 {
		isNotificationSet = true
		n.Body = req.Message
	}

	if len(req.Title) > 0 {
		isNotificationSet = true
		n.Title = req.Title
	}

	if len(req.Image) > 0 {
		isNotificationSet = true
		n.ImageURL = req.Image
	}

	if v, ok := req.Sound.(string); ok && len(v) > 0 {
		isNotificationSet = true
		n.Sound = v
	}

	if isNotificationSet {
		notification.Android.Notification = n
	}

	notificationList := make([]*messaging.MulticastMessage, 0)
	tokens := req.Tokens
	for len(tokens) > 0 {
		newNotification := &messaging.MulticastMessage{}
		newNotification.Data = notification.Data
		newNotification.Android = notification.Android
		if len(tokens) > 500 {
			newNotification.Tokens = tokens[:501]
			tokens = tokens[500:]
		} else {
			newNotification.Tokens = tokens
			tokens = nil
		}

		notificationList = append(notificationList, newNotification)
	}

	return notificationList
}

// PushToAndroid provide send notification to Android server.
func PushToAndroidSDK(req PushNotification) bool {
	LogAccess.Debug("Start push notification for Android")

	var (
		client     *messaging.Client
		retryCount = 0
		maxRetry   = PushConf.Android.MaxRetry
	)

	if req.Retry > 0 && req.Retry < maxRetry {
		maxRetry = req.Retry
	}

	// check message
	err := CheckMessage(req)

	if err != nil {
		LogError.Error("request error: " + err.Error())
		return false
	}

Retry:
	var isError = false

	var newTokens []string
	ctx := context.Background()

	if req.ProjectID != "" {
		opts := make([]option.ClientOption, 0)
		if req.CredentialsFile != "" {
			opts = append(opts, option.WithCredentialsFile(req.CredentialsFile))
		} else if req.CredentialsJSON != "" {
			opts = append(opts, option.WithCredentialsJSON([]byte(req.CredentialsJSON)))
		}
		client, err = InitFCMSDKClient(ctx, &firebase.Config{ProjectID: req.ProjectID}, opts...)
		if err != nil {
			// FCM server error
			LogError.Error("FCM server error: " + err.Error())
			return false
		}
	} else {
		LogError.Error("FCM project id is empty")
		return false
	}

	notificationList := GetAndroidFCMSDKNotification(req)
	for _, notification := range notificationList {
		res, err := client.SendMulticast(ctx, notification)
		if err != nil {
			// Send Message error
			LogError.Error("FCM server send message error: " + err.Error())
			StatStorage.AddAndroidError(int64(len(notification.Tokens)))
			newTokens = append(newTokens, notification.Tokens...)
			for _, to := range notification.Tokens {
				recordErr(to, req, err)
			}
			continue
		}

		if !req.IsTopic() {
			LogAccess.Debug(fmt.Sprintf("Android Success count: %d, Failure count: %d", res.SuccessCount, res.FailureCount))
		}

		StatStorage.AddAndroidSuccess(int64(res.SuccessCount))
		StatStorage.AddAndroidError(int64(res.FailureCount))

		// result from Send messages to specific devices
		for k, result := range res.Responses {
			to := ""
			if k < len(req.Tokens) {
				to = req.Tokens[k]
			} else {
				to = req.To
			}

			if result.Error != nil {
				isError = true
				newTokens = append(newTokens, to)
				recordErr(to, req, result.Error)
				continue
			}

			LogPush(SucceededPush, to, req, nil)
		}

	}
	if isError && retryCount < maxRetry {
		retryCount++

		// resend fail token
		req.Tokens = newTokens
		goto Retry
	}

	return isError
}

func recordErr(to string, req PushNotification, err error) {
	LogPush(FailedPush, to, req, err)
	if PushConf.Core.Sync {
		req.AddLog(getLogPushEntry(FailedPush, to, req, err))
	} else if PushConf.Core.FeedbackURL != "" {
		go func(logger *logrus.Logger, log LogPushEntry, url string, timeout int64) {
			err := DispatchFeedback(log, url, timeout)
			if err != nil {
				logger.Error(err)
			}
		}(LogError, getLogPushEntry(FailedPush, to, req, err), PushConf.Core.FeedbackURL, PushConf.Core.FeedbackTimeout)
	}
}
