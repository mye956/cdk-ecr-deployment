// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/sirupsen/logrus"

	"github.com/aws/aws-lambda-go/cfn"
	"github.com/aws/aws-lambda-go/lambda"

	_ "cdk-ecr-deployment-handler/s3" // Install s3 transport plugin
)

const EnvLogLevel = "LOG_LEVEL"

func init() {
	s, exists := os.LookupEnv(EnvLogLevel)
	if !exists {
		logrus.SetLevel(logrus.InfoLevel)
	} else {
		lvl, err := logrus.ParseLevel(s)
		if err != nil {
			logrus.Errorf("error parsing %s: %v", EnvLogLevel, err)
		}
		logrus.SetLevel(lvl)
	}
}

func handler(ctx context.Context, event cfn.Event) (physicalResourceID string, data map[string]interface{}, err error) {
	physicalResourceID = event.PhysicalResourceID
	data = make(map[string]interface{})

	log.Printf("Event: %s", Dumps(event))

	log.Printf("This is a test log")

	if event.RequestType == cfn.RequestDelete {
		return physicalResourceID, data, nil
	}
	if event.RequestType == cfn.RequestCreate || event.RequestType == cfn.RequestUpdate {
		srcImage, err := getStrProps(event.ResourceProperties, SRC_IMAGE)
		if err != nil {
			return physicalResourceID, data, err
		}
		destImage, err := getStrProps(event.ResourceProperties, DEST_IMAGE)
		if err != nil {
			return physicalResourceID, data, err
		}
		imageArch, err := getStrPropsDefault(event.ResourceProperties, IMAGE_ARCH, "")
		if err != nil {
			return physicalResourceID, data, err
		}
		copyImageIndex, err := getBoolPropsDefault(event.ResourceProperties, COPY_IMAGE_INDEX, false)
		if err != nil {
			return physicalResourceID, data, err
		}
		archImageTags, err := getStrPropsDefault(event.ResourceProperties, ARCH_IMAGE_TAGS, "")
		if err != nil {
			return physicalResourceID, data, err
		}
		srcCreds, err := getStrPropsDefault(event.ResourceProperties, SRC_CREDS, "")
		if err != nil {
			return physicalResourceID, data, err
		}
		destCreds, err := getStrPropsDefault(event.ResourceProperties, DEST_CREDS, "")
		if err != nil {
			return physicalResourceID, data, err
		}
		maxRetries, err := getIntPropsDefault(event.ResourceProperties, MAX_RETRIES, 0)
		if err != nil {
			return physicalResourceID, data, err
		}

		srcCreds, err = parseCreds(srcCreds)
		if err != nil {
			return physicalResourceID, data, err
		}
		destCreds, err = parseCreds(destCreds)
		if err != nil {
			return physicalResourceID, data, err
		}

		log.Printf("SrcImage: %v DestImage: %v ImageArch: %v CopyImageIndex: %v MaxRetries: %v", srcImage, destImage, imageArch, copyImageIndex, maxRetries)

		// Main copy operation
		err = copyImage(srcImage, destImage, srcCreds, destCreds, imageArch, copyImageIndex, maxRetries)
		if err != nil {
			return physicalResourceID, data, err
		}

		// Apply architecture-specific image tags if specified
		if archImageTags != "" {
			err = applyArchImageTags(srcImage, destImage, srcCreds, destCreds, archImageTags, maxRetries)
			if err != nil {
				return physicalResourceID, data, err
			}
		}
	}

	return physicalResourceID, data, nil
}

func main() {
	lambda.Start(cfn.LambdaWrap(handler))
}

func newTimeoutContext() (context.Context, context.CancelFunc) {
	ctx := context.Background()
	var cancel context.CancelFunc = func() {}
	return ctx, cancel
}

func newPolicyContext() (*signature.PolicyContext, error) {
	policy := &signature.Policy{Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()}}
	return signature.NewPolicyContext(policy)
}

func getStrProps(m map[string]interface{}, k string) (string, error) {
	v := m[k]
	val, ok := v.(string)
	if ok {
		return val, nil
	}
	return "", fmt.Errorf("can't get %v", k)
}

func getStrPropsDefault(m map[string]interface{}, k string, d string) (string, error) {
	v := m[k]
	if v == nil {
		return d, nil
	}
	val, ok := v.(string)
	if ok {
		return val, nil
	}
	return "", fmt.Errorf("can't get %v", k)
}

func getBoolPropsDefault(m map[string]interface{}, k string, d bool) (bool, error) {
	v := m[k]
	if v == nil {
		return d, nil
	}
	val, ok := v.(string)
	if ok && (v == "true" || v == "false") {
		return val == "true", nil
	}
	return false, fmt.Errorf(`can't get %v as bool with value %v. valid values are "true" and "false"`, k, v)
}

func getIntPropsDefault(m map[string]interface{}, k string, d int) (int, error) {
	v := m[k]
	if v == nil {
		return d, nil
	}
	val, ok := v.(int)
	if ok && (val >= 0) {
		return val, nil
	}
	return "", fmt.Errorf("can't get %v", k)
}

func parseCreds(creds string) (string, error) {
	credsType := GetCredsType(creds)
	if creds == "" {
		return "", nil
	} else if (credsType == SECRET_ARN) || (credsType == SECRET_NAME) {
		secret, err := GetSecret(creds)
		if err == nil && len(secret) > 0 && json.Valid([]byte(secret)) {
			secret, err = ParseJsonSecret(secret)
		}
		return secret, err
	} else if credsType == SECRET_TEXT {
		return creds, nil
	}
	return "", fmt.Errorf("unkown creds type")
}

func copyImage(srcImage string, destImage string, srcCreds string, destCreds string, imageArch string, copyImageIndex bool, maxRetries int) error {
	log.Printf("Entered copyImage")
	srcRef, err := alltransports.ParseImageName(srcImage)
	if err != nil {
		return err
	}
	destRef, err := alltransports.ParseImageName(destImage)
	if err != nil {
		return err
	}

	srcOpts := NewImageOpts(srcImage, imageArch, copyImageIndex)
	srcOpts.SetCreds(srcCreds)
	srcCtx, err := srcOpts.NewSystemContext()
	if err != nil {
		return err
	}
	destOpts := NewImageOpts(destImage, imageArch, copyImageIndex)
	destOpts.SetCreds(destCreds)
	destCtx, err := destOpts.NewSystemContext()
	if err != nil {
		return err
	}

	ctx, cancel := newTimeoutContext()
	defer cancel()
	policyContext, err := newPolicyContext()
	if err != nil {
		return err
	}
	defer policyContext.Destroy()

	copyOpts := &copy.Options{
		ReportWriter:   os.Stdout,
		DestinationCtx: destCtx,
		SourceCtx:      srcCtx,
	}
	if copyImageIndex {
		copyOpts.ImageListSelection = copy.CopyAllImages
	}

	for i := 0; i <= maxRetries; i++ {
		log.Printf("Attempting to copy image (attempt %d/%d)...", i+1, maxRetries)
		_, err = copy.Image(ctx, policyContext, destRef, srcRef, copyOpts)
		if err == nil {
			log.Printf("Copy succeeded on attempt %d", i+1)
			return nil
		}
		log.Printf("Copy attempt %d failed: %s", i+1, err.Error())
		retriableErr := IsECRRateLimit(err)
		log.Printf("Is the error retriable? %t", retriableErr)
		if retriableErr && i < maxRetries {
			backoff := time.Duration(1<<uint(i)) * 2 * time.Second
			log.Printf("Rate limit hit, retrying in %v...", backoff)
			time.Sleep(backoff)
			continue
		}
		return fmt.Errorf("copy image failed: %s", err.Error())
	}
	return fmt.Errorf("copy image failed after %d retries: %s", maxRetries+1, err.Error())
}

func applyArchImageTags(srcImage string, destImage string, srcCreds string, destCreds string, archImageTags string, maxRetries int) error {
	tags, err := GetImageTagsMap(archImageTags)
	if err != nil {
		return err
	}

	for arch, tag := range tags {
		archDestImage := GetImageDestination(destImage, tag)
		err := copyImage(srcImage, archDestImage, srcCreds, destCreds, arch, false, maxRetries)
		if err != nil {
			return err
		}
	}
	return nil
}
