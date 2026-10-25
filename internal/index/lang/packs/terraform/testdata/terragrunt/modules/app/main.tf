variable "vpc_id" {}

resource "aws_security_group" "app" {
  vpc_id = var.vpc_id
}
