# nubis-prometheus-exposition

This is a small go tool which queries the AWS api and writes a text-based
exposition for Prometheus. It includes metrics for:

- ASG Instances (aws_asg_instances)
- EC2 Instances Tags (aws_ec2_tags)
- EFS Tags (aws_efs_tags)
- ELB Instances (aws_elb_instances)
- Lambda Tags (aws_lambda_tags)
- RDS Tags (aws_rds_tags)

## Usage

### Install Dependancie Management Tool

Install [dep](https://golang.github.io/dep/docs/installation.html)

### Install Dependant Libraries

```bash
dep ensure -v
```

### Compile Application

```bash
make build
```

### Execute Application

```bash
aws-vault exec ACCOUNT-ro -- ./build/(linux|darwin)/nubis-prometheus-exposition --region us-west-2 --out-file ./test.prom
```

## AWS IAM Role Policy

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "ExpositionReadOnly",
            "Effect": "Allow",
            "Action": [
                "ec2:DescribeInstances"
                "elasticloadbalancing:DescribeLoadBalancers",
                "lambda:ListFunctions",
                "lambda:ListTags",
                "autoscaling:DescribeAutoScalingGroups",
                "rds:DescribeDBInstances",
                "elasticfilesystem:DescribeFileSystems"
            ],
            "Resource": "*"
        }
    ]
}
```
