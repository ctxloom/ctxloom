Feature: Version command
  As a user
  I want to check the mlcm version
  So that I know which version I'm using

  Scenario: Display version
    When I run mlcm "version"
    Then the exit code should be 0
    And the output should contain "dev"

  Scenario: Version with -v shorthand
    When I run mlcm "version"
    Then the exit code should be 0

