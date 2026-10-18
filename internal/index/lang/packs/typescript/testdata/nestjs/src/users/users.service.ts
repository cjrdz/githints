import { Injectable } from "@nestjs/common";
import { Repository } from "typeorm";
import { User } from "./entities/user.entity";

@Injectable()
export class UsersService {
  constructor(private readonly repo: Repository<User>) {}
  findAll() {
    return this.repo.find();
  }
  findOne(id: string) {
    return this.repo.findOneBy({ id });
  }
}
