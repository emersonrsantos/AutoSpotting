package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	autospotting "github.com/AutoSpotting/AutoSpotting/core"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	ec2instancesinfo "github.com/cristim/ec2-instances-info"
	"github.com/namsral/flag"
)

type cfgData struct {
	*autospotting.Config
}

var conf *cfgData

// Version represents the build version being used
var Version = "number missing"

func main() {
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		lambda.Start(Handler)
	} else {
		run()
	}
}

func run() {

	log.Println("Starting autospotting agent, build", Version)

	log.Printf("Parsed command line flags: "+
		"regions='%s' "+
		"min_on_demand_number=%d "+
		"min_on_demand_percentage=%.1f "+
		"allowed_instance_types=%v "+
		"disallowed_instance_types=%v "+
		"on_demand_price_multiplier=%.2f "+
		"spot_price_buffer_percentage=%.3f "+
		"bidding_policy=%s "+
		"tag_filters=%s "+
		"tag_filter_mode=%s "+
		"spot_product_description=%v "+
		"instance_termination_method=%s "+
		"termination_notification_action=%s "+
		"cron_schedule=%s\n "+
		"cron_schedule_state=%s\n",
		conf.Regions,
		conf.MinOnDemandNumber,
		conf.MinOnDemandPercentage,
		conf.AllowedInstanceTypes,
		conf.DisallowedInstanceTypes,
		conf.OnDemandPriceMultiplier,
		conf.SpotPriceBufferPercentage,
		conf.BiddingPolicy,
		conf.FilterByTags,
		conf.TagFilteringMode,
		conf.SpotProductDescription,
		conf.InstanceTerminationMethod,
		conf.TerminationNotificationAction,
		conf.CronSchedule,
		conf.CronScheduleState,
	)

	autospotting.Run(conf.Config)
	log.Println("Execution completed, nothing left to do")
}

// this is the equivalent of a main for when running from Lambda, but on Lambda
// the run() is executed within the handler function every time we have an event
func init() {
	var region string

	if r := os.Getenv("AWS_REGION"); r != "" {
		region = r
	} else {
		region = endpoints.UsEast1RegionID
	}

	conf = &cfgData{
		&autospotting.Config{
			LogFile:         os.Stdout,
			LogFlag:         log.Ldate | log.Ltime | log.Lshortfile,
			MainRegion:      region,
			SleepMultiplier: 1,
		},
	}

	conf.initialize()

}

// Handler implements the AWS Lambda handler
func Handler(ctx context.Context, rawEvent json.RawMessage) {

	var snsEvent events.SNSEvent
	var cloudwatchEvent events.CloudWatchEvent
	parseEvent := rawEvent

	// Try to parse event as an Sns Message
	if err := json.Unmarshal(parseEvent, &snsEvent); err != nil {
		log.Println(err.Error())
		return
	}

	// If event is from Sns - extract Cloudwatch's one
	if snsEvent.Records != nil {
		snsRecord := snsEvent.Records[0]
		parseEvent = []byte(snsRecord.SNS.Message)
	}

	// Try to parse event as Cloudwatch Event Rule
	if err := json.Unmarshal(parseEvent, &cloudwatchEvent); err != nil {
		log.Println(err.Error())
		return
	}

	// If event is Instance Spot Interruption
	if cloudwatchEvent.DetailType == "EC2 Spot Instance Interruption Warning" {
		if instanceID, err := autospotting.GetInstanceIDDueForTermination(cloudwatchEvent); err != nil {
			return
		} else if instanceID != nil {
			spotTermination := autospotting.NewSpotTermination(cloudwatchEvent.Region)
			spotTermination.ExecuteAction(instanceID, conf.TerminationNotificationAction)
		}
	} else {
		// Event is Autospotting Cron Scheduling
		run()
	}
}

// Configuration handling
func (c *cfgData) initialize() {

	c.parseCommandLineFlags()

	data, err := ec2instancesinfo.Data()
	if err != nil {
		log.Fatal(err.Error())
	}
	c.InstanceData = data
}

func (c *cfgData) parseCommandLineFlags() {
	flag.StringVar(&c.AllowedInstanceTypes, "allowed_instance_types", "",
		"\n\tIf specified, the spot instances will be searched only among these types.\n\tIf missing, any instance type is allowed.\n"+
			"\tAccepts a list of comma or whitespace separated instance types (supports globs).\n"+
			"\tExample: ./AutoSpotting -allowed_instance_types 'c5.*,c4.xlarge'\n")
	flag.StringVar(&c.BiddingPolicy, "bidding_policy", autospotting.DefaultBiddingPolicy,
		"\n\tPolicy choice for spot bid. If set to 'normal', we bid at the on-demand price(times the multiplier).\n"+
			"\tIf set to 'aggressive', we bid at a percentage value above the spot price \n"+
			"\tconfigurable using the spot_price_buffer_percentage.\n")
	flag.StringVar(&c.DisallowedInstanceTypes, "disallowed_instance_types", "",
		"\n\tIf specified, the spot instances will _never_ be of these types.\n"+
			"\tAccepts a list of comma or whitespace separated instance types (supports globs).\n"+
			"\tExample: ./AutoSpotting -disallowed_instance_types 't2.*,c4.xlarge'\n")
	flag.StringVar(&c.InstanceTerminationMethod, "instance_termination_method", autospotting.DefaultInstanceTerminationMethod,
		"\n\tInstance termination method.  Must be one of '"+autospotting.DefaultInstanceTerminationMethod+"' (default),\n"+
			"\t or 'detach' (compatibility mode, not recommended)\n")
	flag.StringVar(&c.TerminationNotificationAction, "termination_notification_action", autospotting.DefaultTerminationNotificationAction,
		"\n\tTermination Notification Action.\n"+
			"\tValid choices:\n"+
			"\t'"+autospotting.DefaultTerminationNotificationAction+
			"' (terminate if lifecyclehook else detach) | 'terminate' (lifecyclehook triggered)"+
			" | 'detach' (lifecyclehook not triggered)\n")
	flag.Int64Var(&c.MinOnDemandNumber, "min_on_demand_number", autospotting.DefaultMinOnDemandValue,
		"\n\tNumber of on-demand nodes to be kept running in each of the groups.\n\t"+
			"Can be overridden on a per-group basis using the tag "+autospotting.OnDemandNumberLong+".\n")
	flag.Float64Var(&c.MinOnDemandPercentage, "min_on_demand_percentage", 0.0,
		"\n\tPercentage of the total number of instances in each group to be kept on-demand\n\t"+
			"Can be overridden on a per-group basis using the tag "+autospotting.OnDemandPercentageTag+
			"\n\tIt is ignored if min_on_demand_number is also set.\n")
	flag.Float64Var(&c.OnDemandPriceMultiplier, "on_demand_price_multiplier", 1.0,
		"\n\tMultiplier for the on-demand price. Numbers less than 1.0 are useful for volume discounts.\n"+
			"\tExample: ./AutoSpotting -on_demand_price_multiplier 0.6 will have the on-demand price "+
			"considered at 60% of the actual value.\n")
	flag.StringVar(&c.Regions, "regions", "",
		"\n\tRegions where it should be activated (separated by comma or whitespace, also supports globs).\n"+
			"\tBy default it runs on all regions.\n"+
			"\tExample: ./AutoSpotting -regions 'eu-*,us-east-1'\n")
	flag.Float64Var(&c.SpotPriceBufferPercentage, "spot_price_buffer_percentage", autospotting.DefaultSpotPriceBufferPercentage,
		"\n\tBid a given percentage above the current spot price.\n\tProtects the group from running spot"+
			"instances that got significantly more expensive than when they were initially launched\n"+
			"\tThe tag "+autospotting.SpotPriceBufferPercentageTag+" can be used to override this on a group level.\n"+
			"\tIf the bid exceeds the on-demand price, we place a bid at on-demand price itself.\n")
	flag.StringVar(&c.SpotProductDescription, "spot_product_description", autospotting.DefaultSpotProductDescription,
		"\n\tThe Spot Product to use when looking up spot price history in the market.\n"+
			"\tValid choices: Linux/UNIX | SUSE Linux | Windows | Linux/UNIX (Amazon VPC) | \n"+
			"\tSUSE Linux (Amazon VPC) | Windows (Amazon VPC)\n\tDefault value: "+autospotting.DefaultSpotProductDescription+"\n")
	flag.StringVar(&c.TagFilteringMode, "tag_filtering_mode", "opt-in", "\n\tControls the behavior of the tag_filters option.\n"+
		"\tValid choices: opt-in | opt-out\n\tDefault value: 'opt-in'\n\tExample: ./AutoSpotting --tag_filtering_mode opt-out\n")
	flag.StringVar(&c.FilterByTags, "tag_filters", "", "\n\tSet of tags to filter the ASGs on.\n"+
		"\tDefault if no value is set will be the equivalent of -tag_filters 'spot-enabled=true'\n"+
		"\tIn case the tag_filtering_mode is set to opt-out, it defaults to 'spot-enabled=false'\n"+
		"\tExample: ./AutoSpotting --tag_filters 'spot-enabled=true,Environment=dev,Team=vision'\n")

	flag.StringVar(&c.CronSchedule, "cron_schedule", "* *", "\n\tCron-like schedule in which to"+
		"\tperform(or not) spot replacement actions. Format: hour day-of-week\n"+
		"\tExample: ./AutoSpotting --cron_schedule '9-18 1-5' # workdays during the office hours \n")

	flag.StringVar(&c.CronScheduleState, "cron_schedule_state", "on", "\n\tControls whether to take actions "+
		"inside or outside the schedule defined by cron_schedule. Allowed values: on|off\n"+
		"\tExample: ./AutoSpotting --cron_schedule_state='off' --cron_schedule '9-18 1-5'  # would only take action outside the defined schedule\n")

	v := flag.Bool("version", false, "Print version number and exit.\n")
	flag.Parse()
	printVersion(v)
}

func printVersion(v *bool) {
	if *v {
		fmt.Println("AutoSpotting build:", Version)
		os.Exit(0)
	}
}
