require "rails_helper"

RSpec.describe "Articles" do
  it "lists" do
    get "/articles"
  end
end
