package com.example.shop.domain

import jakarta.persistence.Entity
import jakarta.persistence.Id
import org.springframework.data.jpa.repository.JpaRepository

@Entity
open class Order(@Id val id: Long = 0)

interface OrderRepository : JpaRepository<Order, Long>
