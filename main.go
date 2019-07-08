package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	cloudflare "github.com/cloudflare/cloudflare-go"
	"github.com/davecgh/go-spew/spew"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.cfmigrate.yaml)")

	// Cloudflare email
	rootCmd.PersistentFlags().StringP("cfemail", "e", "", "Cloudflare Email Address")
	viper.BindPFlag("cfemail", rootCmd.PersistentFlags().Lookup("cfemail"))

	rootCmd.PersistentFlags().StringP("cfkey", "k", "", "Cloudflare API Key")
	viper.BindPFlag("cfkey", rootCmd.PersistentFlags().Lookup("cfkey"))

	// AWS Key
	rootCmd.PersistentFlags().StringP("awskey", "a", "", "AWS Key")
	viper.BindPFlag("awskey", rootCmd.PersistentFlags().Lookup("awskey"))

	// AWS Secret
	rootCmd.PersistentFlags().StringP("awssecret", "s", "", "AWS Secret Key")
	viper.BindPFlag("awssecret", rootCmd.PersistentFlags().Lookup("awssecret"))

	rootCmd.PersistentFlags().StringVarP(&domain, "domain", "d", "", "Domain name to compare")
}

func main() {
	Execute()
}

var (
	cfgFile string
	domain  string

	// rootCmd represents the base command when called without any subcommands
	rootCmd = &cobra.Command{
		Use:   "cfmigrate",
		Short: "A brief description of your application",
		Long:  ``,
		Run:   doCompare,
	}
)

type (
	record struct {
		Name  string
		Type  string
		TTL   int
		Value []string
	}

	config struct {
		cfemail      string
		cfkey        string
		awskey       string
		awssecret    string
		domain       string
		awsRecordSet []record
		cfRecordSet  []record
		session      *session.Session
		r53          *route53.Route53
		api          *cloudflare.API
	}
)

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// Search config in home directory with name ".cfmigrate" (without extension).
		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.SetConfigName("cfmigrate")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}

func assembleConfig() (*config, error) {
	cfg := &config{
		cfemail:      viper.GetString("cfemail"),
		cfkey:        viper.GetString("cfkey"),
		awskey:       viper.GetString("awskey"),
		awssecret:    viper.GetString("awssecret"),
		domain:       domain,
		awsRecordSet: make([]record, 0),
		cfRecordSet:  make([]record, 0),
	}

	if cfg.cfemail == "" {
		return nil, errors.New("No cloudflare email supplied")
	}

	if cfg.cfkey == "" {
		return nil, errors.New("No cloudflare api key supplied")
	}

	if cfg.awskey == "" {
		return nil, errors.New("No AWS key supplied")
	}

	if cfg.awssecret == "" {
		return nil, errors.New("No AWS Secret Key supplied")
	}

	if cfg.domain == "" {
		return nil, errors.New("No domain name supplied")
	}

	sess := session.New(&aws.Config{
		Credentials: credentials.NewStaticCredentials(cfg.awskey, cfg.awssecret, ""),
	})

	cfg.session = sess
	cfg.r53 = route53.New(cfg.session)

	api, err := cloudflare.New(cfg.cfkey, cfg.cfemail)
	if err != nil {
		return nil, err
	}

	cfg.api = api

	return cfg, nil
}

func checkErr(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func doCompare(cmd *cobra.Command, args []string) {
	cfg, err := assembleConfig()
	checkErr(err)

	// verify domain exists in route53
	var hzid string
	q := fmt.Sprintf("%s.", cfg.domain)
	out, err := cfg.r53.ListHostedZonesByName(&route53.ListHostedZonesByNameInput{
		DNSName: aws.String(q),
	})
	checkErr(err)

	for _, hz := range out.HostedZones {
		if *hz.Config.PrivateZone == false && *hz.Name == q {
			hzid = *hz.Id
			break
		}
	}

	if hzid == "" {
		checkErr(fmt.Errorf("Unable to find domain '%s' in route53", cfg.domain))
	}

	// verify domain exists in cloudflare
	zoneID, err := cfg.api.ZoneIDByName(cfg.domain)
	checkErr(err)

	// Fetch route53 record set
	err = cfg.r53.ListResourceRecordSetsPages(&route53.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(hzid),
	}, func(page *route53.ListResourceRecordSetsOutput, lastPage bool) bool {
		for _, r := range page.ResourceRecordSets {
			// determine if record is a genuine A record or an alias record
			cfg.awsRecordSet = append(cfg.awsRecordSet, record{
				Name: *r.Name,
				Type: *r.Type,
			})
		}
		return true
	})
	checkErr(err)

	// Fetch cloudflare record set
	records, err := cfg.api.DNSRecords(zoneID, cloudflare.DNSRecord{})
	checkErr(err)

	for _, r := range records {
		cfg.cfRecordSet = append(cfg.cfRecordSet, record{
			Name:  r.Name,
			Value: []string{r.Content},
			Type:  r.Type,
			TTL:   r.TTL,
		})
	}

	spew.Dump(cfg.cfRecordSet)
}
