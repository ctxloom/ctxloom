Feature: Version command
  As a user
  I want to check the scm version
  So that I know which version I'm using

  Scenario: Display version
    When I run scm "version"
    Then the exit code should be 0
    And the output should match "(dev|v?[0-9]+\.[0-9]+)"

  Scenario: Version with -v shorthand
    When I run scm "version"
    Then the exit code should be 0

