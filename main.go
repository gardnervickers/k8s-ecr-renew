package main

import (
	"flag"

	"github.com/Sirupsen/logrus"

	"github.com/aws/aws-sdk-go/aws/session"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ecr"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/fields"
)

const (
	dockerJSONTemplate = `{"auths":{"%s":{"auth":"%s","email":"none"}}}`
)

type config struct {
	Kubecfg            string
	KubeMasterURL      string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegion          string
	RefreshInterval    int
}

func parse() config {
	// Flags for
	// Order of auth
	// Check for kubecfg, then kube-master-url, then finally service account token.
	var kubecfg = flag.String("kubecfg", "", "")
	var kubeMasterURL = flag.String("kube-master-url", "", "")
	var awsAccessKeyID = flag.String("aws_access_key_id", "", "")
	var awsSecretAccessKey = flag.String("aws_secret_access_key", "", "")
	var awsRegion = flag.String("aws_region", "", "")
	var refreshInterval = flag.Int("refresh-interval", 60, "")
	flag.Parse()
	return config{*kubecfg, *kubeMasterURL, *awsAccessKeyID,
		*awsSecretAccessKey, *awsRegion, *refreshInterval}
}

func NewKubeClient(kubeCfgFile string) (*kubernetes.Clientset, error) {

	var client *kubernetes.Clientset

	if len(kubeCfgFile) == 0 {
		logrus.Info("Using InCluster k8s config")
		cfg, err := rest.InClusterConfig()

		if err != nil {
			return nil, err
		}

		client, err = kubernetes.NewForConfig(cfg)

		if err != nil {
			return nil, err
		}
	} else {
		logrus.Infof("Using OutOfCluster k8s config with kubeConfigFile: %s", kubeCfgFile)
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeCfgFile)

		if err != nil {
			logrus.Error("Got error trying to create client: ", err)
			return nil, err
		}

		client, err = kubernetes.NewForConfig(cfg)

		if err != nil {
			return nil, err
		}
	}

	return client, nil
}

func getECRSecret(region string, secretName string) *v1.Secret {
	logrus.Infof("Fetching ECR token for region: %s", region)
	sesh := session.Must(session.NewSession())
	awscfg := aws.NewConfig().WithRegion(region)
	ecrClient := ecr.New(sesh, awscfg)
	input := &ecr.GetAuthorizationTokenInput{}
	result, err := ecrClient.GetAuthorizationToken(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ecr.ErrCodeServerException:
				fmt.Println(ecr.ErrCodeServerException, aerr.Error())
			case ecr.ErrCodeInvalidParameterException:
				fmt.Println(ecr.ErrCodeInvalidParameterException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		}
	}
	token := *result.AuthorizationData[0].AuthorizationToken
	endpoint := *result.AuthorizationData[0].ProxyEndpoint
	secret := &v1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name: secretName,
		},
	}
	secret.Data = map[string][]byte{
		".dockerconfigjson": []byte(fmt.Sprintf(dockerJSONTemplate, endpoint, token))}
	secret.Type = "kubernetes.io/dockerconfigjson"
	return secret
}

// Watches all namespaces for changes, executing handler on change.
func WatchNamespaces(client *kubernetes.Clientset, resyncPeriod time.Duration,
	handler func(namespace *v1.Namespace) error) {
	killChan := make(chan struct{})
	_, informer := cache.NewInformer(
		cache.NewListWatchFromClient(client.Core().RESTClient(), "namespaces", v1.NamespaceAll, fields.Everything()),
		&v1.Namespace{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				if err := handler(obj.(*v1.Namespace)); err != nil {
					logrus.Info(err)
				}
			},
			UpdateFunc: func(_ interface{}, obj interface{}) {
				if err := handler(obj.(*v1.Namespace)); err != nil {
					logrus.Info(err)
				}
			},
		},
	)
	informer.Run(killChan)
}

func main() {
	secretName := "ecrsecret"
	cfg := parse()
	logrus.Infof("kubecfg file: %s AWS_ACCESS_KEY_ID: %s", cfg.Kubecfg, cfg.AWSAccessKeyID)
	client, err := NewKubeClient(cfg.Kubecfg)
	if err != nil {
		logrus.Error("Could not create client,", err)
	}
	WatchNamespaces(client, time.Duration(1)*time.Minute, func(ns *v1.Namespace) error {
		if ns.GetDeletionTimestamp() == nil {

			// 2 Update existing service account
			newSecret := getECRSecret(cfg.AWSRegion, secretName)
			// Update secret if it already exists
			_, err := client.Secrets(ns.GetName()).Get(secretName)
			if err == nil {
				logrus.Infof("Found existing secret in ns: %s, updating...", ns.GetName())
				_, updateErr := client.Secrets(ns.GetName()).Update(newSecret)
				if updateErr != nil {
					logrus.Errorf("Error creating secret in ns %s: %s", ns.GetName(), err)
				}
			} else {
				logrus.Infof("Secret does not exist in ns: %s, creating...", ns.GetName())
				_, createErr := client.Secrets(ns.GetName()).Create(newSecret)
				if createErr != nil {
					logrus.Errorf("Error creating secret in ns %s: ", ns.GetName(), err)
					return err
				}
			}

			// Ensure that the default service account exists
			defaultServiceAcccont, defaultServiceErr :=
				client.ServiceAccounts(ns.GetName()).Get("default")
			if err != defaultServiceErr {
				logrus.Errorf("Could not get ServiceAccounts! %v", err)
			}
			imagePullSecretFound := false
			for i, imagePullSecret := range defaultServiceAcccont.ImagePullSecrets {
				if imagePullSecret.Name == newSecret.Name {
					defaultServiceAcccont.ImagePullSecrets[i] = v1.LocalObjectReference{Name: newSecret.Name}
					imagePullSecretFound = true
					break
				}
			}

			if !imagePullSecretFound {
				defaultServiceAcccont.ImagePullSecrets =
					append(defaultServiceAcccont.ImagePullSecrets, v1.LocalObjectReference{Name: newSecret.Name})
			}
			// Update service accounts if they don't contain the secret

			_, err = client.ServiceAccounts(ns.GetName()).Update(defaultServiceAcccont)
			if err != nil {
				return fmt.Errorf("Could update ServiceAccount! %v", err)
			}
			return nil
		}
		return nil
	})
}
