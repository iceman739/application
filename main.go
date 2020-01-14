package main

import (
	"log"
	"os"
	"context"
	"os/signal"
	"syscall"
	
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	typesconfig "github.com/hd-Li/types/config"
	"github.com/rancher/norman/store/proxy"
	"github.com/rancher/norman/store/crd"
	//"github.com/rancher/norman/types"
	projectschema "github.com/hd-Li/types/apis/project.cattle.io/v3/schema"
	projectclient "github.com/hd-Li/types/client/project/v3"
	"github.com/hd-Li/application/controller"
)

var (
	kubeConfig string = "./config" 
)

func main() {
	restConfig, err := rest.InClusterConfig()
	if err != nil{
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			log.Fatalf("Get restconfig failed: %s", err.Error())
			os.Exit(1)
		}
	}
	
	ctx := SigTermCancelContext(context.Background())
	
	userContext, err := typesconfig.NewUserOnlyContext(*restConfig)
	if err != nil {
		log.Fatalf("create userContext failed, err: %s", err.Error())
		os.Exit(1)
	}
	
	err = SetupApplicationCRD(ctx, userContext, *restConfig)
	if err != nil {
		log.Fatalf("ceate application crd failed, err: %s ", err.Error())
		os.Exit(1)
	}
	
	controller.Register(ctx, userContext)
	err = userContext.Start(ctx) 
	if err != nil {
		panic(err)
	}
	<-ctx.Done()
}
// SetupApplicationCRD use for init application crd
func SetupApplicationCRD(ctx context.Context, apiContext *typesconfig.UserOnlyContext, config rest.Config) error {
	//schemas := types.Schemas{}
	
	applicationschema := apiContext.Schemas.Schema(&projectschema.Version, projectclient.ApplicationType)
	//schemas.AddSchema(applicationschema)
	
	clientGetter, err := proxy.NewClientGetterFromConfig(config)
	if err != nil {
		log.Fatalf("create clientGetter error: %s", err.Error())
		return err
	}
	
	factory := &crd.Factory{ClientGetter: clientGetter}
	_, err = factory.CreateCRDs(ctx, typesconfig.UserStorageContext, applicationschema)
	
	return err
}
// SigTermCancelContext use for kill process
func SigTermCancelContext(ctx context.Context) context.Context {
	term := make(chan os.Signal)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(ctx)

	go func() {
		select {
		case <-term:
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx
}
