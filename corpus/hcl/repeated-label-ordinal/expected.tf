provider "aws" {
  region  = "us-west-2"
  profile = "default"
}

provider "aws" {
  alias   = "east"
  region  = "us-east-1"
  profile = "ctxloom"
}
