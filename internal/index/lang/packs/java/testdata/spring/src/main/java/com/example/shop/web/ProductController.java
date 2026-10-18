package com.example.shop.web;

import com.example.shop.domain.Product;
import com.example.shop.domain.ProductRepository;
import org.springframework.web.bind.annotation.*;

@RestController
@RequestMapping("/api/products")
public class ProductController {

    private final ProductRepository repo;

    public ProductController(ProductRepository repo) { this.repo = repo; }

    @GetMapping
    public List<Product> list() { return repo.findAll(); }

    @GetMapping("/{id}")
    public Product one(@PathVariable Long id) { return null; }

    @PostMapping(
        value = "/bulk",
        consumes = "application/json"
    )
    public void bulk(@RequestBody List<Product> products) {}

    // @DeleteMapping("/commented-out")
    private static final String DOC = "@GetMapping(\"/in-a-string\")";
}
