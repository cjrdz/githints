using Microsoft.EntityFrameworkCore;

namespace Shop.Api.Data;

public class ShopContext : DbContext
{
    public DbSet<Product> Products => Set<Product>();
}

public class Product { public int Id { get; set; } }
public record Order(int Id);
