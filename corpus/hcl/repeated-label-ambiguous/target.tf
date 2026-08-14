provider "aws" {
  region  = "us-west-1"
  profile = "default"
}

provider "aws" {
  alias  = "east"
  region = "us-east-1"
}
