Rails.application.routes.draw do
  root "pages#home"
  get "up" => "rails/health#show"
  get "about", to: "pages#about"

  resources :articles do
    resources :comments
  end
  resource :profile

  namespace :admin do
    resources :users
    get "stats", to: "stats#index"
  end

  scope "/api" do
    post "webhooks/:provider", to: "webhooks#create"
  end

  # get "commented-out", to: "x#y"
  delete "logout", to: "sessions#destroy"
end
