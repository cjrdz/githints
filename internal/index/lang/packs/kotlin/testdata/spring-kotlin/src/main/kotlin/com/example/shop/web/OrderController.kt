package com.example.shop.web

import com.example.shop.domain.Order
import com.example.shop.domain.OrderRepository
import org.springframework.web.bind.annotation.*

@RestController
@RequestMapping("/api/orders")
class OrderController(private val repo: OrderRepository) {

    @GetMapping
    fun list(): List<Order> = repo.findAll()

    @PostMapping("/{id}/cancel")
    suspend fun cancel(@PathVariable id: Long) {}
}
