provider "aws" {
  alias  = "brand-new"
  region = "eu-west-1"
}

provider "aws" {
  region  = "us-west-1"
  profile = "default"
}

provider "aws" {
  alias  = "east"
  region = "us-east-1"
}
