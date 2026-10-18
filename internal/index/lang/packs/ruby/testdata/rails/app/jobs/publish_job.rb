class PublishJob < ApplicationJob
  queue_as :default
  def perform(article) = article.publish!
end
