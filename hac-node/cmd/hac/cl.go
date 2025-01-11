package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"github.com/calehh/hac-app/agent"
	"github.com/calehh/hac-app/app"
	app_config "github.com/calehh/hac-app/config"
	cmtconfig "github.com/cometbft/cometbft/config"
	cmtflags "github.com/cometbft/cometbft/libs/cli/flags"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	nm "github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var homeDir string

var clCmd = &cobra.Command{
	Use:   "hac-cl",
	Short: "HAC is a blockchain",
	Long: `A EVM compatible blockchain
                please visit https://hac.io/`,
	Run: func(cmd *cobra.Command, args []string) {
		run(cmd, args)
	},
}

func init() {
	clCmd.Flags().StringVarP(&homeDir, "homedir", "d", "", "home directory")
}

func run(cmd *cobra.Command, args []string) {
	if homeDir == "" {
		homeDir = os.ExpandEnv("$HOME/.hac")
	}

	appConfig := &app_config.Config{
		Config: app_config.DefaultHACCometConfig(),
		App:    app_config.DefaultHACAppConfig(homeDir),
	}

	appConfig.SetRoot(homeDir)
	viper.SetConfigFile(fmt.Sprintf("%s/%s", homeDir, "config/config.toml"))

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Reading config: %v", err)
	}
	if err := viper.Unmarshal(appConfig); err != nil {
		log.Fatalf("Decoding config: %v", err)
	}
	if err := appConfig.ValidateBasic(); err != nil {
		log.Fatalf("Invalid configuration data: %v", err)
	}

	pv := privval.LoadFilePV(
		appConfig.PrivValidatorKeyFile(),
		appConfig.PrivValidatorStateFile(),
	)

	nodeKey, err := p2p.LoadNodeKey(appConfig.NodeKeyFile())
	if err != nil {
		log.Fatalf("failed to load node's key: %v", err)
	}

	logger := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stdout))
	logger, err = cmtflags.ParseLogLevel(appConfig.LogLevel, logger, cmtconfig.DefaultLogLevel)

	if err != nil {
		log.Fatalf("failed to parse log level: %v", err)
	}

	appConfig.App.Home = homeDir
	appConfig.App.TimeoutCommit = uint64(appConfig.Consensus.TimeoutCommit.Seconds())
	app, err := app.NewHACApp(appConfig.App, logger)
	if err != nil {
		log.Fatalf("new App err:%v", err)
	}

	node, err := nm.NewNode(
		appConfig.Config,
		pv,
		nodeKey,
		proxy.NewLocalClientCreator(app),
		nm.DefaultGenesisDocProviderFunc(appConfig.Config),
		cmtconfig.DefaultDBProvider,
		nm.DefaultMetricsProvider(appConfig.Instrumentation),
		logger,
	)

	if err != nil {
		log.Fatalf("Creating node: %v", err)
	}

	app.Start(node.BlockStore())
	err = node.Start()
	if err != nil {
		log.Fatalf("start comet node err %s", err.Error())
	}

	time.Sleep(time.Second * 5)
	if !node.IsRunning() {
		log.Fatal("comet node unable to run")
	}
	// start indexer
	rpcUrl, err := url.Parse(appConfig.Config.RPC.ListenAddress)
	if err != nil {
		log.Fatalf("new parse url err %s", err.Error())
	}
	rpcUrl.Scheme = "http"
	dbPath := path.Join(appConfig.RootDir, "indexer.db")
	indexer, err := agent.NewChainIndexer(logger, dbPath, rpcUrl.String())
	if err != nil {
		log.Fatalf("new chain indexer err %s", err.Error())
	}
	go indexer.Start(context.TODO())

	defer func() {
		log.Println("shut done...")
		done := make(chan struct{})
		go func() {
			defer close(done)
			err = node.Stop()
			if err != nil {
				log.Fatalf("stop comet node err %s", err.Error())
			}
			node.Wait()
			app.Stop()
		}()
		timer := time.NewTimer(time.Second * 10)
		select {
		case <-timer.C:
			os.Exit(1)
		case <-done:
			return
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}