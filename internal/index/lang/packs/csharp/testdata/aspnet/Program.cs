using Shop.Api.Data;
using Shop.Api.Services;

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddScoped<IOrderService, OrderService>();
builder.Services.AddHostedService<CleanupWorker>();

var app = builder.Build();
app.UseMiddleware<RequestTimingMiddleware>();

app.MapGet("/health", () => "ok");

var orders = app.MapGroup("/orders");
var admin = orders.MapGroup("/admin");
orders.MapPost("/", (Order o) => Results.Created());
admin.MapDelete("/{id}", (int id) => Results.NoContent());

// app.MapGet("/commented-out", () => 1);
app.Run();
