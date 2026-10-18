using Shop.Api.Data;

namespace Shop.Api.Services
{
    public interface IOrderService { }
    public class OrderService : IOrderService { }

    public class CleanupWorker : BackgroundService
    {
        protected override Task ExecuteAsync(CancellationToken ct) => Task.CompletedTask;
    }
}
