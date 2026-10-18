using Microsoft.AspNetCore.Mvc;
using Shop.Api.Data;

namespace Shop.Api.Controllers;

[ApiController]
[Route("api/[controller]")]
public class ProductsController : ControllerBase
{
    [HttpGet]
    public IActionResult List() => Ok();

    [HttpGet("{id:int}", Name = "GetProduct")]
    public IActionResult Get(int id) => Ok();

    [HttpPost]
    public IActionResult Create() => Ok();
}
