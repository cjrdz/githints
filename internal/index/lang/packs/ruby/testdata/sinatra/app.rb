require "sinatra"
require_relative "lib/greeter"

get "/" do
  Greeter.hello
end

post "/items/:id" do
  "ok"
end
