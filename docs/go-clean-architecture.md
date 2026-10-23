# How the Booking service is organized in Go

This guide explains the code we have written for listing a hotel's room categories. It is meant to help you connect the Go code to the Java concepts you already know.

## Is Go object oriented?

Go has structs, methods, and interfaces. You can keep data and behavior together and use interfaces for polymorphism. Go does not have Java-style classes or class inheritance; it commonly uses composition instead.

For example, `ListHotelCategories` is a struct that holds its dependencies. `Execute` is a method on that struct:

```go
type ListHotelCategories struct {
    Hotels     HotelRepository
    Categories RoomCategoryRepository
}

func (uc ListHotelCategories) Execute(ctx context.Context, hotelID string) (ListHotelCategoriesResult, error) {
    hotel, found, err := uc.Hotels.GetByID(ctx, hotelID)
    if err != nil {
        return ListHotelCategoriesResult{}, err
    }
    if !found {
        return ListHotelCategoriesResult{}, ErrHotelNotFound
    }

    categories, err := uc.Categories.ListByHotelID(ctx, hotel.ID)
    if err != nil {
        return ListHotelCategoriesResult{}, err
    }
    if categories == nil {
        categories = []domain.RoomCategory{}
    }
    return ListHotelCategoriesResult{Currency: hotel.Currency, Categories: categories}, nil
}
```

The part before the method name, `(uc ListHotelCategories)`, is called a **receiver**. It lets the method access `uc.Hotels` and `uc.Categories`.

## Java concepts and our Go code

| Java concept | Our Go equivalent | Job |
| --- | --- | --- |
| Entity or model class | `domain.Hotel`, `domain.RoomCategory` | Represent booking concepts and their data. |
| Service class and method | `application.ListHotelCategories.Execute` | Complete one user operation. |
| Repository interface | `application.HotelRepository`, `RoomCategoryRepository` | Describe the data operations the use case needs. |
| Repository implementation | A future database adapter | Execute those operations against a database. |
| REST controller | A future handler in `api` | Read HTTP input and write HTTP responses. |

`ListHotelCategories` is an **application service**, also called a **use case**. It is not a separate microservice. The Booking microservice is the whole deployable program containing its domain, application, API, and database code. Auth and Payment will be separate microservices later.

## Why are the repository interfaces beside the use case?

Go files in the same directory normally belong to one package. Here, [repository.go](../internal/booking/application/repository.go) and [list_hotel_categories.go](../internal/booking/application/list_hotel_categories.go) both belong to `package application`, even though they define different kinds of things.

The use case decides which data operations it needs. It currently needs:

```go
type HotelRepository interface {
    GetByID(ctx context.Context, id string) (domain.Hotel, bool, error)
}

type RoomCategoryRepository interface {
    ListByHotelID(ctx context.Context, hotelID string) ([]domain.RoomCategory, error)
}
```

These methods are declared as interfaces in the application layer. Later, a database adapter will implement them. Go does not require an `implements` declaration: a type satisfies an interface when it has the required methods.

This arrangement lets the use case depend on the **operations it needs**, without depending on PostgreSQL, SQL queries, or HTTP.

## A query method versus a use case

Your Java intuition about one method per query applies to the repository. `GetByID` and `ListByHotelID` are separate data access operations.

`Execute` handles the larger request, “list categories for this hotel.” It combines those operations:

1. Ask `HotelRepository` for the hotel.
2. Return a storage error if the lookup fails.
3. Return `ErrHotelNotFound` if the hotel does not exist.
4. Ask `RoomCategoryRepository` for the hotel's categories.
5. Return the hotel's currency and the category list.

The `bool` returned by `GetByID` means “found.” Its `error` means the lookup itself failed. These are different outcomes: a missing hotel becomes HTTP `404`, while a storage failure needs a server error response.

## How one HTTP request will travel

```text
Guest
  -> GET /hotels/{hotelID}/categories
  -> API handler: read hotelID and call the use case
  -> ListHotelCategories.Execute: apply the operation's rules
  -> repository interfaces: request the hotel and categories
  -> database adapters: run the queries
  -> API handler: translate the result or error into HTTP
```

The handler will translate `ErrHotelNotFound` into `404 Not Found`. An existing hotel with no categories will return `200 OK` with an empty categories list. We will choose the exact JSON response shape when we write the handler. This endpoint is a simple listing; date-based availability will be a later operation.

At the moment, the [domain models](../internal/booking/domain), repository interfaces, and use case exist. The HTTP handler and database adapters are still to be built. The next useful check is a use case test with fake repositories, so we can verify its behavior without a database.
