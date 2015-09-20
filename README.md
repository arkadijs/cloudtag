### Cloudtag

VM-s launched from CloudFormation auto-scaling group are hardly distinguishable on purpose. Yet sometimes there are needs to address them individually: for management, for naming them as NS for a DNS zone, etc.

Cloudtag can do two things:

- Tag an AWS VM instance with unique index (Name tag by default, but you may choose another);
- Place machine A record into DNS zone which is handled by Route53.

#### Usage

    $ ./bin/cloudtag.amd64 -h
    Usage: cloudtag [-etcd host[:port]] [-etcd-prefix /cloudtag] [-tag-name Name] [-tag-prefix machine-] [-stack-name coreos-1] [-dns-zone cloud.some] [-delay 0] [-verbose]
        Name tag will be:     {stack-name-}{machine-}{index}
        DNS A record will be: {machine-}{index}{.stack-name}{.dns-zone}
    Typical usage:
        $ AWS_ACCESS_KEY=... AWS_SECRET_KEY=... ./cloudtag -tag-prefix core- -stack-name deis-1 -dns-zone mycontainers.io -delay 30
        AWS credentials are read from
        * environment
        * ~/.aws/credentials
        * instance IAM role (http://169.254.169.254/latest/meta-data/iam/security-credentials/)
    Flags:
      -delay=0: When greater than zero then the instance tag is set again after the delay to combat CloudFormation reseting it
      -dns-zone="": The Route53 DNS zone to insert machine A record into
      -etcd="localhost:4001": The ETCD endpoint
      -etcd-prefix="/cloudtag": The directory in ETCD to use for machine index allocation
      -stack-name="": The name of the stack
      -tag-name="Name": The name of the AWS tag to set
      -tag-prefix="machine-": The prefix to which machine index will be appended
      -verbose=false: Print debug if true

Cloudtag is written in Go, so deployment is easy: you'll find Linux x86_64 binary in `bin/`. Download, `chmod +x`, and you're good to go. See [cloudtag.service] for an example.

In case you do  not want to set the Name or DNS zone, supply empty string `""` to `-tag-name` or `-dns-zone` respectively.

#### Internals

Cloudtag use [etcd] to grab an unique machine index. It meant to be used on [CoreOS] cluster and launched by `systemd` via `cloud-config.yml`.

If you want to rebuild the binary, please use [v4 Signature] enabled [goamz]. Else EC2 Name tagging won't work in eu-central-1 and cn-north-1 regions.

#### Cloud authorization

For AWS authorization it is recommended to use machine [IAM role], for example:

    "IAMRole" : {
      "Type": "AWS::IAM::Role",
      "Properties": {
        "AssumeRolePolicyDocument": {
          "Version" : "2012-10-17",
          "Statement": [{
            "Effect": "Allow",
            "Principal": {
              "Service": [ "ec2.amazonaws.com" ]
            },
            "Action": [ "sts:AssumeRole" ]
          }]
        },
        "Path": "/",
        "Policies": [{
          "PolicyName": "TagInstances",
          "PolicyDocument": {
            "Version": "2012-10-17",
            "Statement": [{
              "Action": ["ec2:DescribeInstances", "ec2:CreateTags", "route53:ListHostedZones", "route53:ChangeResourceRecordSets"],
              "Effect": "Allow",
              "Resource": "*"
            }]
          }
        }]
      }
    },
    "IAMInstanceProfile": {
      "Type": "AWS::IAM::InstanceProfile",
      "Properties": {
        "Path": "/",
        "Roles": [{
          "Ref": "IAMRole"
        }]
      }
    },
    "CoreOSServerLaunchConfig": {
      "Type": "AWS::AutoScaling::LaunchConfiguration",
      "Properties": {
        "IamInstanceProfile" : {"Ref" : "IAMInstanceProfile"},

[CoreOS]: https://coreos.com/
[cloudtag.service]: https://github.com/arkadijs/cloudtag/blob/master/cloudtag.service
[etcd]: https://github.com/coreos/etcd
[IAM role]: http://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-role.html#cfn-iam-role-templateexamples
[v4 Signature]: https://github.com/mitchellh/goamz/pull/154
[goamz]: https://github.com/ekle/goamz
