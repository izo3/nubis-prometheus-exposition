/*
This utility queries the AWS API and generated Prometheus metrics.

Metrics explaned here:
https://prometheus.io/docs/instrumenting/exposition_formats/

Options:
--out-file /some/file
    default: /var/lib/node_exporter/metrics/custom_metrics.prom
--region us-east-1
    default: us-west-2
--help

Build:
go build -o nubis-prometheus-exposition main.go

Run from outside:
aws-vault exec nubis-jd-admin -- ./nubis-prometheus-exposition --out-file ./test.prom

Run from inside:
./nubis-prometheus-exposition --out-file ./test.prom --region us-east-2

*/

package main

import (
    "fmt"
    "strings"
    "bytes"
    "sort"
    "regexp"
    "flag"
    "log"
    "os"
    "path/filepath"
    "time"
    "math/rand"

    "github.com/aws/aws-sdk-go/aws"
    "github.com/aws/aws-sdk-go/aws/session"
    "github.com/aws/aws-sdk-go/service/lambda"
    "github.com/aws/aws-sdk-go/service/autoscaling"
    "github.com/aws/aws-sdk-go/service/elb"
    "github.com/aws/aws-sdk-go/service/ec2"
    "github.com/aws/aws-sdk-go/service/efs"
    "github.com/aws/aws-sdk-go/service/rds"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/common/expfmt"
)

func main() {
    // Set up options
    outFile := flag.String("out-file", "/var/lib/node_exporter/metrics/custom_metrics.prom", "Path to output file for prometheus exposition metrics")
    region := flag.String("region", "us-west-2", "Region to gather metrics for")
    flag.Parse()

    gather_data(*region)
    metricsString := prometheus_gather()
    write_file(*outFile, metricsString)
}

func gather_data(region string) {
    get_asg_membership(region)
    get_ec2_instance_tags(region)
    get_efs_tags(region)
    get_elb_membership(region)
    get_lambda_tags(region)
    get_rds_tags(region)
}

// Create the prometheus regestry
var (
    registry = prometheus.NewRegistry()
)

// Gather all prometheus metrics from the registry
func prometheus_gather () string {
    gatherers := prometheus.Gatherers{
        registry,
    }
    gathering, err := gatherers.Gather()
    if err != nil {
        fmt.Println(err)
    }

    // Create the output buffer and write out all of the gathered metrics
    out := &bytes.Buffer{}
    for _, mf := range gathering {
        if _, err := expfmt.MetricFamilyToText(out, mf); err != nil {
            panic(err)
        }
    }
//    fmt.Print(out.String())
    metricsString := out.String()
    return metricsString
}

// Ensure all Prometheus labels are valid
func sanatize_tag (tag string) string {
    // Metric labels allow only "^[a-zA-Z_][a-zA-Z0-9_]*$"
    // Need to fix tags like: 'aws:autoscaling:groupName'
    regex_full := regexp.MustCompile("^[a-zA-Z_][a-zA-Z0-9_]*$")
    regex_first_number := regexp.MustCompile("^[0-9_]*")
    regex_valid_segment := regexp.MustCompile("[a-zA-Z0-9_]*")

    // Check tag against regex and return if it passes
    if regex_full.MatchString(tag) {
        return tag
    }

    // Slice the string keeping only valid portions
    valid_slice := regex_valid_segment.FindAllStringSubmatch(tag, -1)
    restitch := make([]string, 0, len(valid_slice)+1)
    for i, v := range valid_slice {
        restitch = append(restitch, strings.Join(v[:], ""))
        if i < len(valid_slice)-1 {
            restitch = append(restitch, "_")
        }
    }
    // Restitch string togeather with underscore
    // Validate and return
    valid_string := strings.Join(restitch[:], "")
    if regex_full.MatchString(valid_string) {
        return valid_string
    }

    // Test for the first character matching [0-9] and pad with underscore
    // Validate and retrune else panic
    pad_slice := make([]string, 0, len(valid_string)+1)
    if regex_first_number.MatchString(valid_string) {
        pad_slice = append(pad_slice, "_", valid_string)
    }
    pad_string := strings.Join(pad_slice[:], "")
    if regex_full.MatchString(pad_string) {
        return pad_string
    } else {
        errorString := ("Can not fix tag '" + tag + "' to validate against regex '^[a-zA-Z][a-zA-Z0-9_]*$'")
        panic(errorString)
    }
}

func write_file(outFile string, fileContents string) {
    dir, file := filepath.Split(outFile)
    s1 := rand.NewSource(time.Now().UnixNano())
    r1 := rand.New(s1)
    tmpName := filepath.Join(dir, fmt.Sprintf("%s.tmp%d", file, r1.Intn(10000)))

    tmpFile, err := os.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
    if err != nil {
        log.Fatal(err)
    }

    defer os.Remove(tmpName)

    if _, err := tmpFile.Write([]byte(fileContents)); err != nil {
        log.Fatal(err)
    }
    if err := tmpFile.Close(); err != nil {
        log.Fatal(err)
    }

    if err := os.Rename(tmpName, outFile); err != nil {
        log.Fatal(err)
    }
}

// Lists all instances in an ASG in us-west-2
func get_asg_membership(region string) {
    // Initialize a session
    sess := session.Must(session.NewSessionWithOptions(session.Options{
        SharedConfigState: session.SharedConfigEnable,
    }))

    // Create AutoScaling service client
    svc := autoscaling.New(sess, &aws.Config{Region: aws.String(region)})

    result, err := svc.DescribeAutoScalingGroups(nil)
    if err != nil {
        fmt.Println(err.Error())
        return
    }

    // Create and register a new gauge for prometheus
    asg := prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "aws_asg_instances",
            Help: "Metric per EC2 instances within an ASG.",
        },
        []string{"AutoScalingGroupName", "AutoScalingGroupARN", "InstanceId"},
    )
    registry.MustRegister(asg)

    // Iterate through all groups, gather instances adding a metric for each
    for _, f := range result.AutoScalingGroups {
        for _, v := range f.Instances {
            asg.WithLabelValues(aws.StringValue(f.AutoScalingGroupName), aws.StringValue(f.AutoScalingGroupARN), *v.InstanceId).Set(1)
        }
    }
}

// Lists all tags for all instances in us-west-2
// Iterate through instances to ONLY look up keys and add unique to map
// Create new guage with keys from map
// Iterate through instances making one guage metric each with all key:value pairs populated
func get_ec2_instance_tags(region string) {
    // Initialize a session
    sess := session.Must(session.NewSessionWithOptions(session.Options{
        SharedConfigState: session.SharedConfigEnable,
    }))

    // Create EC2 service client
    svc := ec2.New(sess, &aws.Config{Region: aws.String(region)})

    result, err := svc.DescribeInstances(nil)
    if err != nil {
        fmt.Println(err.Error())
        return
    }

    // Iterate through all the instances, gather the tag names and add them to the tags map
    tags := make(map[string]string)
    for _, f := range result.Reservations {
        if len(f.Instances) > 0 {
            for _, v := range f.Instances {
                if len(v.Tags) > 0 {
                    for _, v := range v.Tags {
                        // If the key is not in the map, add it
                        if _, ok := tags[*v.Key]; ! ok {
                            tags[*v.Key] = ""
                        }
                    }
                }
            }
        }
    }

    // Gather all tags for each instance and pupulate instance map
    instances := make(map[string]map[string]string)
    // Iterate through all the instances and create one entry with all tags
    for _, f := range result.Reservations {
        if len(f.Instances) > 0 {
            for _, i := range f.Instances {
                // Initialize the map for this instance
                instances[*i.InstanceId] = make(map[string]string)

                // Add all keys to the map. It is necessary to have every tag for the metric
                for key, _ := range(tags) {
                    instances[*i.InstanceId][key] = ""
                }

                // Populate the instance's map with the tag values
                if len(i.Tags) > 0 {
                    for _, t := range i.Tags {
                        instances[*i.InstanceId][*t.Key] = *t.Value
                    }
                }
            }
        }
    }

    // Create a string slice of keys for sorting
    keys := make([]string, 0, len(tags)+1)
    keys = append(keys, "InstanceId")
    for k := range(tags) {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    // Make sure all tag names are safe as Prometheus labels
    // Specifically 'aws:autoscaling:groupName' is not valid
    sanitizedKeys := make([]string, 0, len(keys))
    for _, v := range(keys) {
        sanitizeKey := sanatize_tag(v)
        sanitizedKeys = append(sanitizedKeys, sanitizeKey)
    }

    // Create and register a new gauge for prometheus
    ec2 := prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "aws_ec2_tags",
            Help: "Key:Value metric per EC2 instances with all tags.",
        },
        sanitizedKeys,
    )
    registry.MustRegister(ec2)

    // Build sort order []string for each instance
    // Create one metric per instance with sort ordered labels
    for key, value := range(instances) {
        instanceString := make([]string, 0, len(keys))
        for _, v := range(keys) {
            if v == "InstanceId" {
                instanceString = append(instanceString, key)
            } else {
                instanceString = append(instanceString, value[v])
            }
        }
        ec2.WithLabelValues(instanceString...).Set(1)
    }
}

// Lists all EFS tags in us-west-2
func get_efs_tags(region string) {
    // Initialize a session
    sess := session.Must(session.NewSessionWithOptions(session.Options{
        SharedConfigState: session.SharedConfigEnable,
    }))

    // Create EFS service client
    svc := efs.New(sess, &aws.Config{Region: aws.String(region)})

    result, err := svc.DescribeFileSystems(nil)
    if err != nil {
        fmt.Println(err.Error())
        return
    }

    // Iterate through all the filesystems, gather the tag names and add them to the tags map
    tags := make(map[string]string)
    for _, f := range result.FileSystems {
        // Create input for DescribeTags method
        fileSystemId := aws.StringValue(f.FileSystemId)
        input := &efs.DescribeTagsInput{
            FileSystemId: aws.String(fileSystemId),
        }

        // List out the tags
        resultTags, err := svc.DescribeTags(input)
        if err != nil {
            fmt.Println(err.Error())
            return
        }

        // If the key is not in the map, add it
        for _, v := range resultTags.Tags {
            if _, ok := tags[*v.Key]; ! ok {
                tags[*v.Key] = ""
            }
        }
    }

    // Gather all tags for each fileSystem and pupulate fileSystem map
    fileSystem := make(map[string]map[string]string)
    // Iterate through all the fileSystem and create one entry with all tags
    for _, f := range result.FileSystems {
        // Create input for DescribeTags method
        fileSystemId := aws.StringValue(f.FileSystemId)
        input := &efs.DescribeTagsInput{
            FileSystemId: aws.String(fileSystemId),
        }

        // List out the tags
        resultTags, err := svc.DescribeTags(input)
        if err != nil {
            fmt.Println(err.Error())
            return
        }

        // Initialize the map for this FileSystem
        fileSystem[*f.FileSystemId] = make(map[string]string)

        // Add all keys to the map. It is necessary to have every tag for the metric
        for key, _ := range(tags) {
            fileSystem[*f.FileSystemId][key] = ""
        }

        // Populate the fileSystem's map with the tag values
        for _, t := range resultTags.Tags {
            fileSystem[*f.FileSystemId][*t.Key] = *t.Value
        }
    }

    // Create a string slice of keys for sorting
    keys := make([]string, 0, len(tags)+1)
    keys = append(keys, "FileSystemId")
    for k := range(tags) {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    // Make sure all tag names are safe as Prometheus labels
    sanitizedKeys := make([]string, 0, len(keys))
    for _, v := range(keys) {
        sanitizeKey := sanatize_tag(v)
        sanitizedKeys = append(sanitizedKeys, sanitizeKey)
    }

    // Create and register a new gauge for prometheus
    efs := prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "aws_efs_tags",
            Help: "Key:Value metric per EFS fileSystem with all tags.",
        },
        sanitizedKeys,
    )
    registry.MustRegister(efs)

    // Build sort order []string for each filesystem
    // Create one metric per filesystem with sort ordered labels
    for key, value := range(fileSystem) {
        fileSystemString := make([]string, 0, len(keys))
        for _, v := range(keys) {
            if v == "FileSystemId" {
                fileSystemString = append(fileSystemString, key)
            } else {
                fileSystemString = append(fileSystemString, value[v])
            }
        }
        efs.WithLabelValues(fileSystemString...).Set(1)
    }
}

// Lists all instances in an elb in us-west-2
func get_elb_membership(region string) {
    // Initialize a session
    sess := session.Must(session.NewSessionWithOptions(session.Options{
        SharedConfigState: session.SharedConfigEnable,
    }))

    // Create ELB service client
    svc := elb.New(sess, &aws.Config{Region: aws.String(region)})

    result, err := svc.DescribeLoadBalancers(nil)

    if err != nil {
        fmt.Println(err.Error())
        return
    }

    // Create and register a new gauge for prometheus
    elb := prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "aws_elb_instances",
            Help: "Metric per EC2 instances assignd to an ELB.",
        },
        []string{"LoadBalancerName", "DNSName", "InstanceId"},
    )
    registry.MustRegister(elb)

    // Iterate through all groups, gather instances adding a metric for each
    for _, f := range result.LoadBalancerDescriptions {
        for _, v := range f.Instances {
            elb.WithLabelValues(aws.StringValue(f.LoadBalancerName), aws.StringValue(f.DNSName), *v.InstanceId).Set(1)
        }
    }
}

// Lists all Lambda functions in us-west-2
func get_lambda_tags(region string) {
    // Initialize a session
    sess := session.Must(session.NewSessionWithOptions(session.Options{
        SharedConfigState: session.SharedConfigEnable,
    }))

    // Create Lambda service client
    svc := lambda.New(sess, &aws.Config{Region: aws.String(region)})

    result, err := svc.ListFunctions(nil)
    if err != nil {
        fmt.Println(err.Error())
        return
    }

    // Iterate through all the functions, gather the tag names and add them to the tags map
    tags := make(map[string]string)
    for _, f := range result.Functions {
        // Create input for ListTags method
        arn := aws.StringValue(f.FunctionArn)
        input := &lambda.ListTagsInput{
            Resource: aws.String(arn),
        }
        // List out the tags
        resultTags, err := svc.ListTags(input)
        if err != nil {
            fmt.Println(err.Error())
            return
        }

        // If the key is not in the map, add it
        for k, _ := range resultTags.Tags {
            if _, ok := tags[k]; ! ok {
                tags[k] = ""
            }
        }
    }

    // Gather all tags for each function and pupulate function map
    function := make(map[string]map[string]string)
    // Iterate through all the functions and create one entry with all tags
    for _, f := range result.Functions {
        // Create input for ListTags method
        arn := aws.StringValue(f.FunctionArn)
        input := &lambda.ListTagsInput{
            Resource: aws.String(arn),
        }
        // List out the tags
        resultTags, err := svc.ListTags(input)
        if err != nil {
            fmt.Println(err.Error())
            return
        }

        // Initialize the map for this FileSystem
        function[*f.FunctionArn] = make(map[string]string)

        // Add all keys to the map. It is necessary to have every tag for the metric
        for key, _ := range(tags) {
            function[*f.FunctionArn][key] = ""
        }

        // Add metadata as tags
        function[*f.FunctionArn]["FunctionName"] = aws.StringValue(f.FunctionName)
        function[*f.FunctionArn]["Description"] = aws.StringValue(f.Description)

        // Populate the function's map with the tag values
        for k, v := range resultTags.Tags {
            function[*f.FunctionArn][k] = *v
        }
    }

    // Create a string slice of keys for sorting
    keys := make([]string, 0, len(tags)+1)
    keys = append(keys, "FunctionArn")
    keys = append(keys, "FunctionName")
    keys = append(keys, "Description")
    for k := range(tags) {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    // Make sure all tag names are safe as Prometheus labels
    sanitizedKeys := make([]string, 0, len(keys))
    for _, v := range(keys) {
        sanitizeKey := sanatize_tag(v)
        sanitizedKeys = append(sanitizedKeys, sanitizeKey)
    }

    // Create and register a new gauge for prometheus
    lambda := prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "aws_lambda_tags",
            Help: "Key:Value metric per Lambda function with all tags.",
        },
        sanitizedKeys,
    )
    registry.MustRegister(lambda)

    // Build sort order []string for each filesystem
    // Create one metric per filesystem with sort ordered labels
    for key, value := range(function) {
        functionString := make([]string, 0, len(keys))
        for _, v := range(keys) {
            if v == "FunctionArn" {
                functionString = append(functionString, key)
            } else {
                functionString = append(functionString, value[v])
            }
        }
        lambda.WithLabelValues(functionString...).Set(1)
    }
}

// Lists all RDS tags in us-west-2
func get_rds_tags(region string) {
    // Initialize a session
    sess := session.Must(session.NewSessionWithOptions(session.Options{
        SharedConfigState: session.SharedConfigEnable,
    }))

    // Create RDS service client
    svc := rds.New(sess, &aws.Config{Region: aws.String(region)})

    result, err := svc.DescribeDBInstances(nil)
    if err != nil {
        fmt.Println(err.Error())
        return
    }

    // Iterate through all the dBInstances, gather the tag names and add them to the tags map
    tags := make(map[string]string)
    for _, f := range result.DBInstances {
        // Create input for ListTagsForResource method
        resourceName := aws.StringValue(f.DBInstanceArn)
        input := &rds.ListTagsForResourceInput{
            ResourceName: aws.String(resourceName),
        }

        // List out the tags
        resultTags, err := svc.ListTagsForResource(input)
        if err != nil {
            fmt.Println(err.Error())
            return
        }

        // If the key is not in the map, add it
        for _, v := range resultTags.TagList {
            if _, ok := tags[*v.Key]; ! ok {
                tags[*v.Key] = ""
            }
        }
    }

    // Gather all tags for each dbInstance and pupulate dbInstance map
    dbInstance := make(map[string]map[string]string)
    for _, f := range result.DBInstances {
        // Create input for ListTagsForResource method
        resourceName := aws.StringValue(f.DBInstanceArn)
        input := &rds.ListTagsForResourceInput{
            ResourceName: aws.String(resourceName),
        }

        // List out the tags
        resultTags, err := svc.ListTagsForResource(input)
        if err != nil {
            fmt.Println(err.Error())
            return
        }

        // Initialize the map for this dbInstance
        dbInstance[*f.DBInstanceArn] = make(map[string]string)

        // Add all keys to the map. It is necessary to have every tag for the metric
        for key, _ := range(tags) {
            dbInstance[*f.DBInstanceArn][key] = ""
        }

        // Add metadata as tags
        dbInstance[*f.DBInstanceArn]["DBName"] = aws.StringValue(f.DBName)
        dbInstance[*f.DBInstanceArn]["DBInstanceIdentifier"] = aws.StringValue(f.DBInstanceIdentifier)

        // Populate the dbInstance's map with the tag values
        for _, t := range resultTags.TagList {
            dbInstance[*f.DBInstanceArn][*t.Key] = *t.Value
        }
    }

    // Create a string slice of keys for sorting
    keys := make([]string, 0, len(tags)+1)
    keys = append(keys, "DBInstanceArn")
    keys = append(keys, "DBName")
    keys = append(keys, "DBInstanceIdentifier")
    for k := range(tags) {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    // Make sure all tag names are safe as Prometheus labels
    sanitizedKeys := make([]string, 0, len(keys))
    for _, v := range(keys) {
        sanitizeKey := sanatize_tag(v)
        sanitizedKeys = append(sanitizedKeys, sanitizeKey)
    }

    // Create and register a new gauge for prometheus
    rds := prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "aws_rds_tags",
            Help: "Key:Value metric per RDS instance with all tags.",
        },
        sanitizedKeys,
    )
    registry.MustRegister(rds)

    // Build sort order []string for each dbInstance
    // Create one metric per dbInstance with sort ordered labels
    for key, value := range(dbInstance) {
        dbInstanceString := make([]string, 0, len(keys))
        for _, v := range(keys) {
            if v == "DBInstanceArn" {
                dbInstanceString = append(dbInstanceString, key)
            } else {
                dbInstanceString = append(dbInstanceString, value[v])
            }
        }
        rds.WithLabelValues(dbInstanceString...).Set(1)
    }
}
