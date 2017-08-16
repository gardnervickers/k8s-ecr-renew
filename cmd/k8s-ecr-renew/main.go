package main

const (
	dockerCfgTemplate                = `{"%s":{"username":"oauth2accesstoken","password":"%s","email":"none"}}`
	dockerJSONTemplate               = `{"auths":{"%s":{"auth":"%s","email":"none"}}}`
	dockerPrivateRegistryPasswordKey = "DOCKER_PRIVATE_REGISTRY_PASSWORD"
	dockerPrivateRegistryServerKey   = "DOCKER_PRIVATE_REGISTRY_SERVER"
	dockerPrivateRegistryUserKey     = "DOCKER_PRIVATE_REGISTRY_USER"
)

var ()

func main() {

}
